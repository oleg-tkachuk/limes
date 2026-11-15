package biscuit

import (
	"context"
	"errors"
	"testing"

	biscuitgo "github.com/eclipse-biscuit/biscuit-go/v2"

	"github.com/oleg-tkachuk/limes"
)

// Two JWK thumbprints a block may bind a copy to.
const (
	thumbprintA = "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"
	thumbprintB = "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"
)

// withFacts is token with a block of facts appended.
func withFacts(t *testing.T, token string, facts ...biscuitgo.Fact) string {
	t.Helper()
	return appendBlock(t, token, func(t *testing.T, blk biscuitgo.BlockBuilder) {
		for _, f := range facts {
			if err := blk.AddFact(f); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// A block that names a single-valued fact twice with different values says
// two things at once, which another reader of the Biscuit could take
// either way: it is refused rather than read as its last value.
func TestBiscuitRefusesASingleValuedFactNamedTwice(t *testing.T) {
	s, _, token := biscuitFixture(t)
	for name, facts := range map[string][]biscuitgo.Fact{
		"two bindings": {
			biscuitFact(biscuitFactBind, biscuitgo.String(thumbprintA)),
			biscuitFact(biscuitFactBind, biscuitgo.String(thumbprintB)),
		},
		"two request limits": {
			biscuitFact(biscuitFactMaxRequests, biscuitgo.Integer(1)),
			biscuitFact(biscuitFactMaxRequests, biscuitgo.Integer(2)),
		},
		"two budgets": {
			biscuitFact(biscuitFactMaxBudget, biscuitgo.Integer(1)),
			biscuitFact(biscuitFactMaxBudget, biscuitgo.Integer(2)),
		},
	} {
		tok := withFacts(t, token, facts...)
		if _, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, limes.ErrBiscuitAttenuation) {
			t.Errorf("%s: err = %v, want ErrBiscuitAttenuation", name, err)
		}
	}
}

// A block that names a single-valued fact once is read as before.
func TestBiscuitReadsASingleValuedFactNamedOnce(t *testing.T) {
	s, _, token := biscuitFixture(t)
	tok := withFacts(t, token, biscuitFact(biscuitFactMaxRequests, biscuitgo.Integer(1)))
	if _, err := s.verifier.Verify(context.Background(), limes.VerifyRequest{Token: tok, Audience: audienceData}); err != nil {
		t.Errorf("a block limiting its requests once: %v", err)
	}
}
