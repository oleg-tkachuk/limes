package limes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPrincipalValidate(t *testing.T) {
	tenant := uuid.New()
	agent := &AgentPrincipal{AgentType: "worker"}
	valid := map[string]Principal{
		"user":               {Type: PrincipalUser, TenantID: tenant, Subject: "u"},
		"service":            {Type: PrincipalService, TenantID: tenant, Subject: "s"},
		"agent":              {Type: PrincipalAgent, TenantID: tenant, Subject: "a"},
		"agent with details": {Type: PrincipalAgent, TenantID: tenant, Subject: "a", Agent: agent},
	}
	for name, p := range valid {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: Validate = %v, want nil", name, err)
		}
	}
	invalid := map[string]Principal{
		"no type":                {TenantID: tenant, Subject: "x"},
		"unknown type":           {Type: "robot", TenantID: tenant, Subject: "x"},
		"type in another case":   {Type: "Agent", TenantID: tenant, Subject: "x"},
		"agent fields on a user": {Type: PrincipalUser, TenantID: tenant, Subject: "u", Agent: agent},
	}
	for name, p := range invalid {
		if err := p.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: Validate = %v, want ErrInvalidRequest", name, err)
		}
	}
}

func TestPrincipalTypeValidate(t *testing.T) {
	for _, typ := range []PrincipalType{PrincipalUser, PrincipalAgent, PrincipalService} {
		if err := typ.Validate(); err != nil {
			t.Errorf("%q: Validate = %v, want nil", typ, err)
		}
	}
	for _, typ := range []PrincipalType{PrincipalUnspecified, "robot", "Agent"} {
		if err := typ.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%q: Validate = %v, want ErrInvalidRequest", typ, err)
		}
	}
}

// Issue and Delegate refuse a subject Validate refuses.
func TestIssueAndDelegateValidateTheSubject(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	if _, _, err := issuer.issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: "robot", TenantID: uuid.New(), Subject: "a"},
		Audience: []string{"data"},
		Caveats:  Caveats{Ops: []Op{OpGet, OpShare}},
		TTL:      time.Minute,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Issue with an unknown principal type: err = %v, want ErrInvalidRequest", err)
	}

	parent := issueRoot(t, issuer, Caveats{Ops: []Op{OpGet, OpShare}})
	if _, _, err := issuer.delegate(ctx, DelegateRequest{
		Parent:  asVerified(parent),
		Subject: Principal{Type: PrincipalService, Subject: "s", Agent: &AgentPrincipal{AgentType: "worker"}},
		Caveats: Caveats{Ops: []Op{OpGet, OpShare}},
		TTL:     time.Minute,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Delegate to a service with agent fields: err = %v, want ErrInvalidRequest", err)
	}
}

// A token whose principal Validate refuses is not one this module issues, so
// the verifier refuses it as it refuses any other malformed claim.
func TestVerifyRefusesAnUnknownPrincipalType(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	now := time.Now().UTC()
	token := signUnchecked(t, issuer.signer, Capability{
		ID: uuid.New(), Issuer: "limes-test",
		Subject:  Principal{Type: "robot", TenantID: uuid.New(), Subject: "a"},
		Audience: []string{"data"}, Caveats: Caveats{Ops: []Op{OpGet}},
		IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(time.Minute), Generation: 1,
	})
	if _, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: "data"}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Verify of an unknown principal type: err = %v, want ErrInvalidToken", err)
	}
}

// A capability crosses the API by value: changing the request Issue or
// Delegate was given — or the parent a child inherited caveats from — changes
// nothing in the capability they returned.
func TestIssueAndDelegateKeepNoAliasOfTheCaller(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	req := IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a", Agent: &AgentPrincipal{AgentType: "worker"}},
		Audience: []string{"data"},
		Caveats: Caveats{
			Ops: []Op{OpGet, OpShare}, ResourcePrefixes: []string{"a/"}, ResourceURIs: []string{"a/b"},
			SourceIPCIDR: []string{"10.0.0.0/8"},
		},
		TTL: time.Minute,
	}
	parent, _, err := issuer.issue(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := issuer.delegate(ctx, DelegateRequest{
		Parent: asVerified(parent), Subject: Principal{Type: PrincipalAgent, Subject: "child"}, InheritCaveats: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantParent, wantChild := snapshot(parent), snapshot(child)

	req.Audience[0] = "other"
	req.Caveats.Ops[0] = OpDelete
	req.Caveats.ResourcePrefixes[0] = "z/"
	req.Caveats.ResourceURIs[0] = "z/y"
	req.Caveats.SourceIPCIDR[0] = "0.0.0.0/0"
	req.Subject.Agent.AgentType = "rogue"
	if got := snapshot(parent); got != wantParent {
		t.Errorf("the issued capability changed with its request:\n got %s\nwant %s", got, wantParent)
	}

	parent.Caveats.Ops[0] = OpDelete
	parent.Caveats.ResourcePrefixes[0] = "z/"
	if got := snapshot(child); got != wantChild {
		t.Errorf("the delegated capability changed with its parent:\n got %s\nwant %s", got, wantChild)
	}
}

// snapshot is a capability's slices and pointers as text.
func snapshot(c Capability) string {
	agent := ""
	if c.Subject.Agent != nil {
		agent = c.Subject.Agent.AgentType
	}
	return fmt.Sprint(c.Audience, c.Caveats.Ops, c.Caveats.ResourcePrefixes, c.Caveats.ResourceURIs,
		c.Caveats.SourceIPCIDR, agent)
}

// Every slice or pointer a Caveats or Principal holds is one clone copies:
// a field added later and left out of clone would alias the caller's again.
func TestCloneCoversEverySharedField(t *testing.T) {
	for _, v := range []any{Caveats{}, Principal{}, Capability{}, CopyCeiling{}} {
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			field := typ.Field(i)
			switch field.Type.Kind() {
			case reflect.Slice, reflect.Pointer, reflect.Map:
				if !clonedFields[typ.Name()+"."+field.Name] {
					t.Errorf("%s.%s is shared by reference and not in clonedFields; copy it in clone", typ.Name(), field.Name)
				}
			default: // a value: copied with the struct
			}
		}
	}
}

// clonedFields are the reference fields clone copies.
var clonedFields = map[string]bool{
	"Caveats.Ops": true, "Caveats.ResourcePrefixes": true, "Caveats.ResourceURIs": true,
	"Caveats.SourceIPCIDR": true, "Principal.Agent": true,
	"Capability.Audience": true, "Capability.Copies": true, "CopyCeiling.RevocationID": true,
	"CopyCeiling.Requests": true, "CopyCeiling.Budget": true,
}

// asVerified is c as the verifier would have accepted it, for tests of what
// Delegate does with a parent, whatever made it.
func asVerified(c Capability) VerifiedCapability { return VerifiedCapability{c: c} }

// A parent the verifier did not make — the zero VerifiedCapability, the only
// one code outside the package can write — delegates nothing.
func TestDelegateRefusesAParentTheVerifierDidNotMake(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, _, err := issuer.delegate(context.Background(), DelegateRequest{
		Subject: Principal{Type: PrincipalAgent, Subject: "child"}, InheritCaveats: true,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Delegate from a zero VerifiedCapability: err = %v, want ErrInvalidRequest", err)
	}
}

// What the verifier accepted cannot be widened through the copy it hands out.
func TestVerifiedCapabilityHandsOutACopy(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	_, token := issueFor(t, issuer)
	vc, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	c := vc.Capability()
	c.Caveats.Ops[0] = OpDelete
	c.Audience[0] = "elsewhere"
	if got := vc.Capability(); got.Caveats.Ops[0] != OpGet || got.Audience[0] != audienceData {
		t.Errorf("a change to the copy reached the verified capability: %+v", got)
	}
}
