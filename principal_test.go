package limes

import (
	"context"
	"errors"
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

// Issue and Delegate refuse a subject Validate refuses.
func TestIssueAndDelegateValidateTheSubject(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	if _, _, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: "robot", TenantID: uuid.New(), Subject: "a"},
		Audience: []string{"data"},
		Caveats:  Caveats{Ops: []Op{OpGet}},
		TTL:      time.Minute,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Issue with an unknown principal type: err = %v, want ErrInvalidRequest", err)
	}

	parent := issueRoot(t, issuer, Caveats{Ops: []Op{OpGet}})
	if _, _, err := issuer.Delegate(ctx, DelegateRequest{
		Parent:  parent,
		Subject: Principal{Type: PrincipalService, Subject: "s", Agent: &AgentPrincipal{AgentType: "worker"}},
		Caveats: Caveats{Ops: []Op{OpGet}},
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
	token, err := issuer.signer.Sign(ctx, Capability{
		ID: uuid.New(), Issuer: "limes-test",
		Subject:  Principal{Type: "robot", TenantID: uuid.New(), Subject: "a"},
		Audience: []string{"data"}, Caveats: Caveats{Ops: []Op{OpGet}},
		IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(time.Minute), Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: "data"}); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("Verify of an unknown principal type: err = %v, want ErrInvalidSignature", err)
	}
}
