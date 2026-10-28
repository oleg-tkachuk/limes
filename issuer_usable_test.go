package limes

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A capability whose request ceiling allows no request can be put to no use:
// Issue and Delegate refuse it as invalid caveats, and record nothing. A
// ceiling of some requests, or none, is issued; a budget of nothing stays a
// capability that may not spend.
func TestIssueAndDelegateRefuseACapabilityNoRequestCanUse(t *testing.T) {
	for name, tc := range map[string]struct {
		requests RequestCeiling
		budget   BudgetCeiling
		refused  bool
	}{
		"no request":            {RequestCeiling{}, BudgetCeiling{}, true},
		"one request":           {RequestCeiling{Max: 1}, BudgetCeiling{}, false},
		"unlimited requests":    {RequestCeiling{Unlimited: true}, BudgetCeiling{}, false},
		"no request, a budget":  {RequestCeiling{}, BudgetCeiling{Max: 1, UnitCode: AbstractUnitCode}, true},
		"requests, no spending": {RequestCeiling{Max: 1}, BudgetCeiling{}, false},
	} {
		store := newMemStore()
		iss := newTermsIssuer(t, store)
		req := validIssue()
		req.Caveats.Ops = []Op{OpGet, OpShare}
		req.Caveats.Requests, req.Caveats.Budget = tc.requests, tc.budget
		_, err := iss.Issue(context.Background(), req)
		if refused := errors.Is(err, ErrInvalidCaveats) && errors.Is(err, ErrInvalidRequest); refused != tc.refused || (err != nil && !refused) {
			t.Errorf("%s: Issue = %v, want refused %v", name, err, tc.refused)
		}
		if tc.refused && len(store.caps) != 0 {
			t.Errorf("%s: a refused capability was recorded", name)
		}

		parent := validIssue()
		parent.Caveats.Ops = []Op{OpGet, OpShare}
		root, err := iss.Issue(context.Background(), parent)
		if err != nil {
			t.Fatal(err)
		}
		_, err = iss.Delegate(context.Background(), DelegateRequest{
			Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(root.Capability),
			Caveats: Caveats{Ops: []Op{OpGet}, Requests: tc.requests, Budget: tc.budget}, TTL: time.Minute,
		})
		if refused := errors.Is(err, ErrInvalidCaveats); refused != tc.refused || (err != nil && !refused) {
			t.Errorf("%s: Delegate = %v, want refused %v", name, err, tc.refused)
		}
	}
}

// The request ceiling is checked after the rest of the caveats and before
// the key binding.
func TestValidateTermsChecksTheRequestCeilingAfterTheCaveats(t *testing.T) {
	malformed := Caveats{Ops: []Op{""}}
	if got, want := validateTerms([]string{goldenAudience}, malformed, "x"), malformed.Validate(); got == nil || got.Error() != want.Error() {
		t.Errorf("malformed caveats: validateTerms = %v, want %v", got, want)
	}
	if got := validateTerms([]string{goldenAudience}, Caveats{Ops: []Op{OpGet}}, "x"); !errors.Is(got, ErrInvalidCaveats) {
		t.Errorf("no request and a bad binding: validateTerms = %v, want ErrInvalidCaveats", got)
	}
}
