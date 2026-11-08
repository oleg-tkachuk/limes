package limes

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// A store's refusal to record a lineage too deep or a tenant erased is a
// revocation on the verify path; at a mint it is the refusal of the mint,
// matching nothing of the revoked family. Any other error passes as it is.
func TestMintRefusal(t *testing.T) {
	errDown := errors.New("connection reset")
	for name, tc := range map[string]struct {
		stored   error
		want     error
		code     Code
		status   int
		category Category
	}{
		"a chain too deep": {fmt.Errorf("%w: c would have more than %d ancestors", ErrDelegationTooDeep, MaxDelegationDepth),
			ErrDelegationDepthExceeded, CodeDelegationDepthExceeded, http.StatusForbidden, CategoryDenied},
		"a tenant erased":  {fmt.Errorf("%w: t", ErrTenantErased), ErrTenantGone, CodeTenantGone, http.StatusGone, CategoryNotFound},
		"a deleted tenant": {fmt.Errorf("%w: t", ErrTenantDeleted), ErrTenantDeleted, CodeTenantDeleted, http.StatusGone, CategoryNotFound},
		"a wider child":    {fmt.Errorf("%w: expiry", ErrDelegationTooWide), ErrDelegationTooWide, CodeDelegationTooWide, http.StatusForbidden, CategoryDenied},
	} {
		t.Run(name, func(t *testing.T) {
			err := mintRefusal(tc.stored)
			if !errors.Is(err, tc.want) {
				t.Fatalf("mintRefusal(%v) = %v, want %v", tc.stored, err, tc.want)
			}
			if errors.Is(err, ErrRevoked) || errors.Is(err, ErrUnauthenticated) {
				t.Errorf("mintRefusal(%v) = %v matches the revoked family", tc.stored, err)
			}
			if _, refused := ReasonOf(err); refused {
				t.Errorf("mintRefusal(%v) = %v has a Reason: a mint refuses no presented capability", tc.stored, err)
			}
			p, ok := ProblemOf(err)
			if !ok || p.Code != tc.code || p.Status != tc.status || p.Category != tc.category {
				t.Errorf("mintRefusal(%v): problem %+v, %v; want %s, %d, %s", tc.stored, p, ok, tc.code, tc.status, tc.category)
			}
		})
	}
	if err := mintRefusal(errDown); !errors.Is(err, errDown) || err.Error() != errDown.Error() {
		t.Errorf("mintRefusal(%v) = %v, want it unchanged", errDown, err)
	}
}
