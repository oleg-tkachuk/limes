package limes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
	"uuid"
)

// errStoreDown is a revocation store that does not answer.
var errStoreDown = errors.New("dial tcp: connection refused")

// failingLookup answers every revocation lookup with err.
type failingLookup struct {
	*memStore
	err error
}

func (l failingLookup) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, l.err }

// A revocation lookup that fails refuses the token as unavailable — 503, to
// retry, its cause kept — as the issuer's keys out of reach are; one whose
// failure is a code of limes's already keeps that code.
func TestARevocationLookupFailingIsUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, tc := range map[string]struct {
		lookupErr error
		want      error
		code      Code
		status    int
		category  Category
	}{
		"the store unreachable": {errStoreDown, ErrRevocationUnavailable, CodeRevocationUnavailable, http.StatusServiceUnavailable, CategoryUnavailable},
		"the context ended":     {context.DeadlineExceeded, ErrRevocationUnavailable, CodeRevocationUnavailable, http.StatusServiceUnavailable, CategoryUnavailable},
		"a lineage too deep":    {ErrDelegationTooDeep, ErrDelegationTooDeep, CodeDelegationTooDeep, http.StatusUnauthorized, CategoryUnauthenticated},
		"a broken invariant":    {fmt.Errorf("%w: a row of no tenant", ErrInternal), ErrInternal, CodeInternal, http.StatusInternalServerError, CategoryInternal},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			issuer, verifier, store, _ := buildIssuerVerifier(t)
			res, err := issuer.Issue(ctx, IssueRequest{ClientID: testClientID,
				IssuedBy: Principal{Subject: "test-operator"},
				Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
				Audience: []string{audienceData},
				Caveats:  Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}},
				TTL:      time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			lookup := failingLookup{memStore: store, err: tc.lookupErr}
			verifier.cfg.Revocations = lookup
			_, err = verifier.Verify(ctx, VerifyRequest{Token: res.Token, Audience: audienceData})
			if !errors.Is(err, tc.want) || !errors.Is(err, tc.lookupErr) {
				t.Fatalf("Verify = %v; want %v, wrapping the lookup's %v", err, tc.want, tc.lookupErr)
			}
			if p, ok := ProblemOf(err); !ok || p.Code != tc.code || p.Status != tc.status || p.Category != tc.category {
				t.Errorf("ProblemOf = %+v, %v; want code %q, status %d, category %q", p, ok, tc.code, tc.status, tc.category)
			}
			if resp := verifier.ErrorResponse(ctx, err, AuthSchemeBearer); resp.Status != tc.status {
				t.Errorf("ErrorResponse answers %d, want %d", resp.Status, tc.status)
			}

			issuer.store = lookup
			if _, err := issuer.Reissue(ctx, res.Capability.ID); !errors.Is(err, tc.want) || !errors.Is(err, tc.lookupErr) {
				t.Errorf("Reissue = %v; want %v, wrapping the lookup's %v", err, tc.want, tc.lookupErr)
			}
		})
	}
}

// The refusal for revocations out of reach lifts once they answer again.
func TestRevocationUnavailableIsNoPermanentRefusal(t *testing.T) {
	t.Parallel()
	reason, ok := ReasonOf(fmt.Errorf("%w: lookup: %w", ErrRevocationUnavailable, errStoreDown))
	if !ok || reason != ReasonRevocationUnavailable || reason.Permanent() {
		t.Errorf("ReasonOf = %q, %v, permanent %v; want %q, not permanent", reason, ok, reason.Permanent(), ReasonRevocationUnavailable)
	}
	if errors.Is(ErrRevocationUnavailable, ErrUnauthenticated) {
		t.Error("ErrRevocationUnavailable is an ErrUnauthenticated: the token is not at fault")
	}
}
