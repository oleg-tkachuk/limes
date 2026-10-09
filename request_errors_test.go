package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// failingStore is genStore whose Insert fails as a store does: an outage, or
// a tenant it does not hold.
type failingStore struct {
	genStore
	err error
}

func (s failingStore) Insert(context.Context, Capability, Principal) error { return s.err }

func validIssue() IssueRequest {
	return IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalService, TenantID: uuid.MustParse(goldenTenant), Subject: "svc"},
		Audience: []string{goldenAudience},
		Caveats:  Caveats{Ops: []Op{OpGet}},
		TTL:      time.Hour,
	}
}

// A malformed request came back as an untyped error, so a consumer could not
// tell it from a store outage and answered both as a bad request. Every check
// on the request itself now matches ErrInvalidRequest.
func TestIssueRefusesAMalformedRequestAsInvalid(t *testing.T) {
	iss := testIssuer(t, goldenKID, ed25519.NewKeyFromSeed(goldenSeed[:]), time.Now)
	for name, mutate := range map[string]func(*IssueRequest){
		"no tenant":              func(r *IssueRequest) { r.Subject.TenantID = uuid.Nil },
		"no issuer principal":    func(r *IssueRequest) { r.IssuedBy = Principal{} },
		"a negative generation":  func(r *IssueRequest) { r.Generation = -1 },
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
			if _, _, err := iss.Issue(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
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
			signer, err := NewEd25519Signer(goldenKID, ed25519.NewKeyFromSeed(goldenSeed[:]))
			if err != nil {
				t.Fatal(err)
			}
			iss, err := NewIssuer(IssuerConfig{Signer: signer, Store: failingStore{err: tc.err}, IssuerName: goldenIssuer, DefaultTTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = iss.Issue(context.Background(), validIssue())
			if !errors.Is(err, tc.want) || errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want %v and not ErrInvalidRequest", err, tc.want)
			}
			parent := Capability{ID: uuid.New(), Subject: validIssue().Subject, Audience: []string{goldenAudience},
				Caveats: Caveats{Ops: []Op{OpGet}}, ExpiresAt: time.Now().Add(time.Hour)}
			_, _, err = iss.Delegate(context.Background(), DelegateRequest{Parent: parent, InheritCaveats: true, TTL: time.Minute})
			if !errors.Is(err, tc.want) || errors.Is(err, ErrInvalidRequest) {
				t.Errorf("delegate err = %v, want %v and not ErrInvalidRequest", err, tc.want)
			}
		})
	}
}

func TestDelegateRefusesAMalformedRequestAsInvalid(t *testing.T) {
	iss := testIssuer(t, goldenKID, ed25519.NewKeyFromSeed(goldenSeed[:]), time.Now)
	parent := Capability{ID: uuid.New(), Subject: validIssue().Subject, Audience: []string{goldenAudience},
		Caveats: Caveats{Ops: []Op{OpGet}}, ExpiresAt: time.Now().Add(time.Hour)}
	for name, req := range map[string]DelegateRequest{
		"no parent":              {InheritCaveats: true},
		"an empty audience":      {Parent: parent, Audience: []string{""}, InheritCaveats: true},
		"invalid caveats":        {Parent: parent},
		"a malformed thumbprint": {Parent: parent, InheritCaveats: true, ConfirmationJKT: "nope"},
		"a negative TTL":         {Parent: parent, InheritCaveats: true, TTL: -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := iss.Delegate(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
