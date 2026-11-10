package limes

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"
)

// The texts no store can keep as they are: one with a NUL, one that is not
// UTF-8.
const (
	textNUL     = "a\x00b"
	textNotUTF8 = "a\xffb"
)

// unstorable are the texts every request refuses.
var unstorable = []string{textNUL, textNotUTF8}

// storableCapability is a capability InsertRequest.Validate accepts.
func storableCapability() Capability {
	return Capability{
		ID: uuid.New(), Issuer: "issuer", ExpiresAt: time.Now().Add(time.Hour), ClientID: "client",
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent", Agent: &AgentPrincipal{AgentType: "t"}},
		Audience: []string{"svc"},
		Caveats:  Caveats{Ops: []Op{OpGet}, ResourcePrefixes: []string{"docs/"}, Budget: BudgetCeiling{Unlimited: true, UnitCode: "USD"}},
	}
}

// Every request that hands a store text refuses, as ErrInvalidRequest, text
// a store cannot keep, in each field that carries it; the same request with
// plain text in that field is accepted.
func TestRequestsRefuseUnstorableText(t *testing.T) {
	tenant := uuid.New()
	cases := map[string]func(s string) error{
		"insert subject": func(s string) error {
			c := storableCapability()
			c.Subject.Subject = s
			return InsertRequest{Capability: c}.Validate()
		},
		"insert agent model": func(s string) error {
			c := storableCapability()
			c.Subject.Agent.Model = s
			return InsertRequest{Capability: c}.Validate()
		},
		"insert issuer": func(s string) error {
			c := storableCapability()
			c.Issuer = s
			return InsertRequest{Capability: c}.Validate()
		},
		"insert audience": func(s string) error {
			c := storableCapability()
			c.Audience = []string{s}
			return InsertRequest{Capability: c}.Validate()
		},
		"insert client": func(s string) error {
			c := storableCapability()
			c.ClientID = s
			return InsertRequest{Capability: c}.Validate()
		},
		"insert prior actor": func(s string) error {
			c := storableCapability()
			c.PriorActors = []string{s}
			return InsertRequest{Capability: c}.Validate()
		},
		"insert op": func(s string) error {
			c := storableCapability()
			c.Caveats.Ops = []Op{Op("x:" + s)}
			return InsertRequest{Capability: c}.Validate()
		},
		"insert resource prefix": func(s string) error {
			c := storableCapability()
			c.Caveats.ResourcePrefixes = []string{s}
			return InsertRequest{Capability: c}.Validate()
		},
		"insert on behalf of": func(s string) error {
			c := storableCapability()
			c.OnBehalfOf = &Principal{Type: PrincipalTypeUser, TenantID: c.Subject.TenantID, Subject: s}
			return InsertRequest{Capability: c}.Validate()
		},
		"insert issued by": func(s string) error {
			return InsertRequest{Capability: storableCapability(), IssuedBy: Principal{Subject: s}}.Validate()
		},
		"insert session": func(s string) error { return InsertRequest{Capability: storableCapability(), Session: s}.Validate() },
		"insert subject issuer": func(s string) error {
			return InsertRequest{Capability: storableCapability(), SubjectIssuer: s}.Validate()
		},
		"principal":    func(s string) error { return Principal{Type: PrincipalTypeUser, Subject: s}.Validate() },
		"revoke note":  func(s string) error { return RevokeRequest{CapabilityID: uuid.New(), Note: s}.Validate() },
		"revoke actor": func(s string) error { return RevokeRequest{CapabilityID: uuid.New(), Actor: s}.Validate() },
		"list by principal": func(s string) error {
			return ListByPrincipalRequest{TenantID: tenant, PrincipalType: PrincipalTypeUser, Subject: "u" + s}.Validate()
		},
		"list records subject":    func(s string) error { return ListRecordsRequest{TenantID: tenant, Subject: s}.Validate() },
		"list records prefix":     func(s string) error { return ListRecordsRequest{TenantID: tenant, SubjectPrefix: s}.Validate() },
		"list records client":     func(s string) error { return ListRecordsRequest{TenantID: tenant, ClientID: s}.Validate() },
		"count records session":   func(s string) error { return CountRecordsRequest{TenantID: tenant, Session: s}.Validate() },
		"count records issuer":    func(s string) error { return CountRecordsRequest{TenantID: tenant, SubjectIssuer: s}.Validate() },
		"revoke matching subject": func(s string) error { return RevokeMatchingRequest{TenantID: tenant, Subject: "u" + s}.Validate() },
		"revoke matching note": func(s string) error {
			return RevokeMatchingRequest{TenantID: tenant, ClientID: "c", Note: s}.Validate()
		},
		"erase tenant actor": func(s string) error { return EraseTenantRequest{TenantID: tenant, Actor: s}.Validate() },
		"revoke biscuit note": func(s string) error {
			return RevokeBiscuitRequest{Copy: BiscuitCopy{capabilityID: uuid.New(), revocationID: []byte{1}}, Note: s}.Validate()
		},
		"exchange key token id": func(s string) error { return ExchangeKey{TenantID: tenant, Issuer: "idp", TokenID: "j" + s}.Validate() },
	}
	for name, validate := range cases {
		if err := validate("plain"); err != nil {
			t.Errorf("%s with plain text: %v", name, err)
		}
		for _, s := range unstorable {
			if err := validate(s); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("%s with %q: %v, want ErrInvalidRequest", name, s, err)
			}
		}
	}
}

// countingStore records how many capabilities it was asked to keep.
type countingStore struct{ inserts int }

func (s *countingStore) Insert(context.Context, InsertRequest) error { s.inserts++; return nil }

func (*countingStore) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, nil }

// The issuer refuses text a store cannot keep before it signs: no token
// exists for a capability that could not be recorded, and the store is
// never asked to keep it.
func TestIssueRefusesUnstorableTextBeforeSigning(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	store := &countingStore{}
	issuer, err := NewIssuer(IssuerConfig{Signer: signer, Store: store, IssuerName: "issuer"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(s string) IssueRequest {
		return IssueRequest{
			ClientID: "client", IssuedBy: Principal{Subject: "operator"}, Audience: []string{"svc"}, TTL: time.Minute,
			Subject: Principal{Type: PrincipalTypeService, TenantID: uuid.New(), Subject: "svc" + s},
			Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}},
		}
	}
	for _, s := range unstorable {
		res, err := issuer.Issue(context.Background(), request(s))
		if !errors.Is(err, ErrInvalidRequest) || res.Token != "" {
			t.Errorf("Issue with subject text %q: token %q, %v; want ErrInvalidRequest and no token", s, res.Token, err)
		}
	}
	if store.inserts != 0 {
		t.Errorf("the store was asked to keep %d capabilities", store.inserts)
	}
	for _, field := range []func(*IssueRequest, string){
		func(r *IssueRequest, s string) { r.ClientID = s },
		func(r *IssueRequest, s string) { r.Session = s },
		func(r *IssueRequest, s string) { r.IssuedBy.Subject = s },
		func(r *IssueRequest, s string) { r.Audience = []string{s} },
	} {
		req := request("")
		field(&req, textNUL)
		if res, err := issuer.Issue(context.Background(), req); !errors.Is(err, ErrInvalidRequest) || res.Token != "" {
			t.Errorf("Issue with a NUL: token %q, %v; want ErrInvalidRequest and no token", res.Token, err)
		}
	}
	if store.inserts != 0 {
		t.Errorf("the store was asked to keep %d capabilities", store.inserts)
	}
	if _, err := issuer.Issue(context.Background(), request("")); err != nil || store.inserts != 1 {
		t.Errorf("Issue with plain text: %v, %d inserts", err, store.inserts)
	}
}
