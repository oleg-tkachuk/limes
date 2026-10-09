package limes

import (
	"errors"
	"fmt"
	"testing"
)

func TestReasonOf(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		want      Reason
		permanent bool
	}{
		{"forged", fmt.Errorf("%w: bad", ErrInvalidSignature), ReasonInvalidSignature, true},
		{"unknown kid stays a bad signature", fmt.Errorf("%w: kid %q: %w", ErrInvalidSignature, "k1", ErrUnknownKID), ReasonInvalidSignature, true},
		{"keys unreachable", fmt.Errorf("%w: kid %q: %w", ErrInvalidSignature, "k1", ErrJWKSUnavailable), ReasonKeysUnavailable, false},
		{"expired", ErrExpired, ReasonExpired, true},
		{"not yet valid", ErrNotYetValid, ReasonNotYetValid, false},
		{"revoked", fmt.Errorf("wrapped: %w", ErrRevoked), ReasonRevoked, true},
		{"audience", ErrAudienceMismatch, ReasonAudienceMismatch, true},
		{"biscuit block", fmt.Errorf("%w: rule", ErrBiscuitAttenuation), ReasonInvalidBiscuit, true},
		{"copy limits unmetered", ErrCopyCountersNotMetered, ReasonInvalidBiscuit, true},
		{"proof missing", ErrDPoPRequired, ReasonProofRequired, false},
		{"proof invalid", fmt.Errorf("%w: htm", ErrDPoPInvalid), ReasonProofInvalid, false},
		{"proof replayed", ErrDPoPReplayed, ReasonProofReplayed, false},
		{"op", fmt.Errorf("%w: %q", ErrOpNotAllowed, "put"), ReasonOpNotAllowed, true},
		{"resource", ErrResourceNotAllowed, ReasonResourceNotAllowed, true},
		{"source", ErrSourceIPNotAllowed, ReasonSourceNotAllowed, true},
		{"idempotency key", ErrIdempotencyKeyRequired, ReasonIdempotencyKeyRequired, false},
		{"tainted read", ErrTaintedReadNotAllowed, ReasonTaintedReadNotAllowed, false},
		{"request limit", ErrRequestLimitExceeded, ReasonRequestLimitExceeded, true},
		{"budget", fmt.Errorf("%w: ancestor x", ErrBudgetExceeded), ReasonBudgetExceeded, false},
		{"tenant budget", ErrTenantBudgetExceeded, ReasonTenantBudgetExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReasonOf(tc.err)
			if !ok || got != tc.want {
				t.Fatalf("ReasonOf = %q, %v; want %q", got, ok, tc.want)
			}
			if got.Permanent() != tc.permanent {
				t.Fatalf("%q.Permanent() = %v, want %v", got, got.Permanent(), tc.permanent)
			}
		})
	}
}

// What is not a refusal of the capability has no reason: the consumer maps it
// as any other error, never as a denial.
func TestReasonOfNonRefusals(t *testing.T) {
	for name, err := range map[string]error{
		"nil":             nil,
		"invalid request": ErrInvalidRequest,
		"effect conflict": ErrEffectConflict,
		"invalid amount":  ErrInvalidAmount,
		"store failure":   errors.New("connection reset"),
	} {
		if r, ok := ReasonOf(err); ok {
			t.Errorf("%s: ReasonOf = %q, want none", name, r)
		}
	}
	if Reason("no_such_reason").Permanent() {
		t.Error("an unknown reason must not be permanent")
	}
}

// Every reason is named once and maps back from its own sentinel, so the
// order of the table cannot shadow one.
func TestReasonsAreDistinctAndReachable(t *testing.T) {
	seen := map[Reason]bool{}
	for _, r := range Reasons() {
		if seen[r] {
			t.Errorf("reason %q listed twice", r)
		}
		seen[r] = true
	}
	for _, rule := range reasonRules {
		if got, _ := ReasonOf(rule.sentinel); got != rule.reason {
			t.Errorf("sentinel %v resolves to %q, shadowed before %q", rule.sentinel, got, rule.reason)
		}
	}
}
