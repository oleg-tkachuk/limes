package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
	"uuid"
)

// failingStore is genStore whose Insert fails as a store does: an outage, or
// a tenant it does not hold.
type failingStore struct {
	genStore
	err error
}

func (s failingStore) Insert(context.Context, InsertRequest) error { return s.err }

func validIssue() IssueRequest {
	return IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalTypeService, TenantID: uuid.MustParse(goldenTenant), Subject: "svc"},
		Audience: []string{goldenAudience},
		Caveats:  Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}},
		TTL:      time.Hour,
	}
}

// A malformed request came back as an untyped error, so a consumer could not
// tell it from a store outage and answered both as a bad request. Every check
// on the request itself now matches ErrInvalidRequest.
func TestIssueRefusesAMalformedRequestAsInvalid(t *testing.T) {
	iss := testIssuer(t, goldenKID, ed25519.NewKeyFromSeed(goldenSeed[:]), time.Now)
	for name, mutate := range map[string]func(*IssueRequest){
		"no tenant":              func(r *IssueRequest) { r.Subject.TenantID = uuid.Nil() },
		"no issuer principal":    func(r *IssueRequest) { r.IssuedBy = Principal{} },
		"no audience":            func(r *IssueRequest) { r.Audience = nil },
		"an empty audience":      func(r *IssueRequest) { r.Audience = []string{""} },
		"invalid caveats":        func(r *IssueRequest) { r.Caveats = Caveats{} },
		"a malformed thumbprint": func(r *IssueRequest) { r.ConfirmationJKT = "not a thumbprint" },
		"a negative TTL":         func(r *IssueRequest) { r.TTL = -time.Second },
		"not-before past expiry": func(r *IssueRequest) { r.NotBefore = time.Now().Add(2 * time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			req := validIssue()
			mutate(&req)
			if _, _, err := iss.issue(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestInvalidCaveatsIsAnInvalidRequest(t *testing.T) {
	if !errors.Is(ErrInvalidCaveats, ErrInvalidRequest) {
		t.Error("ErrInvalidCaveats does not match ErrInvalidRequest")
	}
	if errors.Is(ErrInvalidRequest, ErrInvalidCaveats) {
		t.Error("every invalid request matched ErrInvalidCaveats")
	}
}

// What the store reports is not the request's fault: it must not read as one,
// and the store's own sentinel must survive the issuer's wrapping.
func TestIssueKeepsTheStoreErrorApartFromTheRequest(t *testing.T) {
	outage := errors.New("connection refused")
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"an unknown tenant": {ErrUnknownTenant, ErrUnknownTenant},
		"a deleted tenant":  {ErrTenantDeleted, ErrTenantDeleted},
		"an outage":         {outage, outage},
	} {
		t.Run(name, func(t *testing.T) {
			signer, err := NewSigner(SignerConfig{KeyID: goldenKID, Key: SealKey(ed25519.NewKeyFromSeed(goldenSeed[:]))})
			if err != nil {
				t.Fatal(err)
			}
			iss, err := NewIssuer(IssuerConfig{Signer: signer, Store: failingStore{err: tc.err}, IssuerName: goldenIssuer, DefaultTTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = iss.issue(context.Background(), validIssue())
			if !errors.Is(err, tc.want) || errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want %v and not ErrInvalidRequest", err, tc.want)
			}
			parent := Capability{ID: uuid.New(), Issuer: goldenIssuer, Subject: validIssue().Subject, Audience: []string{goldenAudience},
				Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}}, ExpiresAt: time.Now().Add(time.Hour), ClientID: testClientID}
			_, _, err = iss.delegate(context.Background(), DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true, TTL: time.Minute})
			if !errors.Is(err, tc.want) || errors.Is(err, ErrInvalidRequest) {
				t.Errorf("delegate err = %v, want %v and not ErrInvalidRequest", err, tc.want)
			}
		})
	}
}

func TestDelegateRefusesAMalformedRequestAsInvalid(t *testing.T) {
	iss := testIssuer(t, goldenKID, ed25519.NewKeyFromSeed(goldenSeed[:]), time.Now)
	parent := Capability{ClientID: testClientID, ID: uuid.New(), Issuer: goldenIssuer, Subject: validIssue().Subject, Audience: []string{goldenAudience},
		Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}}, ExpiresAt: time.Now().Add(time.Hour)}
	for name, req := range map[string]DelegateRequest{
		"no parent":              {InheritCaveats: true},
		"an empty audience":      {Parent: asVerified(parent), Audience: []string{""}, InheritCaveats: true},
		"invalid caveats":        {Parent: asVerified(parent)},
		"a malformed thumbprint": {Parent: asVerified(parent), InheritCaveats: true, ConfirmationJKT: "nope"},
		"a negative TTL":         {Parent: asVerified(parent), InheritCaveats: true, TTL: -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			// A valid subject, so each case is refused for its own fault.
			req.Subject = Principal{Type: PrincipalTypeAgent, Subject: "child"}
			if _, _, err := iss.delegate(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// revocationOutageStore is genStore whose revocation lookup fails.
type revocationOutageStore struct {
	genStore
	err error
}

func (s revocationOutageStore) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	return false, s.err
}

// Nothing is derived from a capability the store cannot say is not revoked:
// no Biscuit is sealed and no child delegated, and the store's error comes
// back as itself, never as a revoked capability or a bad request.
func TestDerivingKeepsTheStoreErrorApart(t *testing.T) {
	outage := errors.New("connection refused")
	signer, err := NewSigner(SignerConfig{KeyID: goldenKID, Key: SealKey(ed25519.NewKeyFromSeed(goldenSeed[:]))})
	if err != nil {
		t.Fatal(err)
	}
	iss, err := NewIssuer(IssuerConfig{
		Signer: signer, Store: revocationOutageStore{err: outage}, IssuerName: goldenIssuer, DefaultTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := validIssue()
	req.Caveats.Ops = []Op{OpGet, OpShare}
	c, _, err := iss.issue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	parent := VerifiedCapability{c: c}
	for name, derive := range map[string]func() error{
		"a Biscuit": func() error {
			_, err := iss.Biscuit(context.Background(), BiscuitRequest{Capability: parent})
			return err
		},
		"a delegation": func() error {
			_, err := iss.Delegate(context.Background(), DelegateRequest{
				Parent: parent, Subject: Principal{Type: PrincipalTypeService, Subject: "child"}, InheritCaveats: true,
			})
			return err
		},
	} {
		if err := derive(); !errors.Is(err, outage) || errors.Is(err, ErrRevoked) || errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want the store's error alone", name, err)
		}
	}
}
