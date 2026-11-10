package limes

import (
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"
)

// secret is a value the client or the parent carries, which no detail may
// repeat.
const secret = "s3cret-value"

// A refusal of the client's own input carries a detail naming the part it
// refused and the rule it broke, and none of the values involved; other
// codes carry none.
func TestProblemDetail(t *testing.T) {
	parent := Capability{
		ID: uuid.New(), Audience: []string{"aud-" + secret}, ExpiresAt: time.Now().Add(time.Hour),
		Subject: Principal{TenantID: uuid.New()},
		Caveats: Caveats{Ops: []Op{OpGet}, ResourcePrefixes: []string{"docs/" + secret}, Requests: RequestCeiling{Unlimited: true}},
	}
	wider := parent
	wider.Caveats.Ops = []Op{OpGet, OpPut}
	otherPrefix := parent
	otherPrefix.Caveats.ResourcePrefixes = []string{"other/" + secret}
	_, amountErr := ParseAmount("x" + secret)
	for name, tc := range map[string]struct {
		err    error
		code   Code
		detail string
	}{
		"a constant refusal of a request": {RevokeRequest{}.Validate(), CodeInvalidRequest, "revoke: capability id required"},
		"an op the parent lacks":          {Narrows(parent, wider), CodeDelegationTooWide, "an op is not among the parent's"},
		"a prefix the parent lacks":       {Narrows(parent, otherPrefix), CodeDelegationTooWide, "a resource prefix is not under the parent's"},
		"a malformed op": {
			Caveats{Ops: []Op{Op(secret)}, Requests: RequestCeiling{Unlimited: true}}.Validate(),
			CodeInvalidCaveats, "an op is neither built in nor namespaced as \"<namespace>:<name>\"",
		},
		"a resource with a control character": {
			Caveats{Ops: []Op{OpGet}, ResourcePrefixes: []string{secret + "\x01"}, Requests: RequestCeiling{Unlimited: true}}.Validate(),
			CodeInvalidCaveats, "a resource contains a control character",
		},
		"an amount that is no number": {amountErr, CodeInvalidAmount, "the amount is not a decimal number"},
		"a negative amount":           {Nanos(-1).Validate(), CodeInvalidAmount, "the amount is negative or past the largest one"},
	} {
		if tc.err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		p, ok := ProblemOf(tc.err)
		if !ok || p.Code != tc.code || p.Detail != tc.detail {
			t.Errorf("%s: %s %q, want %s %q", name, p.Code, p.Detail, tc.code, tc.detail)
		}
		if strings.Contains(p.Detail, secret) || strings.Contains(p.Title, secret) {
			t.Errorf("%s: the problem repeats a value: %+v", name, p)
		}
	}
}

// The error's text keeps the values the server's log needs: the detail is
// added beside it, not in its place.
func TestProblemDetailKeepsTheText(t *testing.T) {
	parent := Capability{
		Audience: []string{"svc"}, ExpiresAt: time.Now().Add(time.Hour),
		Caveats: Caveats{Ops: []Op{OpGet}, ResourcePrefixes: []string{"docs/" + secret}, Requests: RequestCeiling{Unlimited: true}},
	}
	child := parent
	child.Caveats.ResourcePrefixes = []string{"other/"}
	if err := Narrows(parent, child); err == nil || !strings.Contains(err.Error(), secret) || !errors.Is(err, ErrDelegationTooWide) {
		t.Errorf("Narrows: %v, want the parent's prefix in its text", err)
	}
}

// Only the codes DetailCodes names carry a detail: a refusal of another
// kind, a detail on it or not, is answered without one.
func TestProblemDetailOnlyForClientInput(t *testing.T) {
	for _, err := range []error{
		refuse(ErrInternal, "an internal detail", "broken"),
		refuse(ErrInvalidConfig, "a configuration detail", "misconfigured"),
		refuse(ErrRevoked, "a revocation detail", "revoked"),
		ErrInvalidToken,
	} {
		if p, _ := ProblemOf(err); p.Detail != "" {
			t.Errorf("%v: detail %q on code %s", err, p.Detail, p.Code)
		}
	}
	if p, _ := ProblemOf(errors.New("not limes's")); p.Detail != "" {
		t.Errorf("an error not limes's: detail %q", p.Detail)
	}
}
