package limes_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// An issuance for a tenant whose usage was erased is refused as the tenant
// gone — 410, not found — while the meter's refusal of a capability of that
// tenant stays a revocation: one presents a capability, the other none.
func TestIssueForAnErasedTenantIsGone(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	c, _ := s.issue()
	subject, err := limes.StoredMeterSubject(ctx, s.records, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.EraseTenantUsage(ctx, meter.EraseTenantUsageRequest{TenantID: s.tenantID}); err != nil {
		t.Fatal(err)
	}

	_, err = s.issuer.Issue(ctx, limes.IssueRequest{
		ClientID: exampleClientID,
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "late"},
		Audience: []string{exampleAudience},
		TTL:      time.Minute,
		Caveats:  limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}},
	})
	if !errors.Is(err, limes.ErrTenantGone) || errors.Is(err, limes.ErrTenantErased) || errors.Is(err, limes.ErrUnauthenticated) {
		t.Fatalf("Issue for an erased tenant: err = %v; want ErrTenantGone, matching neither ErrTenantErased nor ErrUnauthenticated", err)
	}
	p, ok := limes.ProblemOf(err)
	if !ok || p.Status != http.StatusGone || p.Code != limes.CodeTenantGone || p.Category != limes.CategoryNotFound || p.Reason != "" {
		t.Errorf("Issue for an erased tenant: problem %+v, %v; want 410 %s, not_found, no reason", p, ok, limes.CodeTenantGone)
	}

	_, err = s.usage.Bump(ctx, meter.BumpRequest{Subject: subject}, nil)
	if !errors.Is(err, limes.ErrTenantErased) || !errors.Is(err, limes.ErrRevoked) {
		t.Errorf("Bump for an erased tenant: err = %v; want ErrTenantErased, matching ErrRevoked", err)
	}
	if code, _ := limes.CodeOf(err); code != limes.CodeTenantErased {
		t.Errorf("Bump for an erased tenant: code %q, want %q", code, limes.CodeTenantErased)
	}
	if reason, _ := limes.ReasonOf(err); reason != limes.ReasonRevoked {
		t.Errorf("Bump for an erased tenant: reason %q, want %q", reason, limes.ReasonRevoked)
	}
}
