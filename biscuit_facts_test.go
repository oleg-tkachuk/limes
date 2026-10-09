package limes

import (
	"strings"
	"testing"
	"time"
)

// Every fact refuses a term of the wrong kind, and a block's facts combine
// as documented on blockFacts.
func TestReadBlockFacts(t *testing.T) {
	jkt := strings.Repeat("A", 43) // a base64url SHA-256 thumbprint
	early, late := time.Unix(1_800_000_000, 0).UTC(), time.Unix(1_900_000_000, 0).UTC()
	fact := func(name string, term any) decodedFact { return decodedFact{name: name, term: term} }

	refused := map[string]decodedBlock{
		"rules or checks":       {others: 1},
		"op not a string":       {facts: []decodedFact{fact(biscuitFactOp, int64(1))}},
		"empty prefix":          {facts: []decodedFact{fact(biscuitFactResourcePrefix, "")}},
		"uri not a string":      {facts: []decodedFact{fact(biscuitFactResourceURI, early)}},
		"plane not a string":    {facts: []decodedFact{fact(biscuitFactPlane, int64(1))}},
		"expiry not a date":     {facts: []decodedFact{fact(biscuitFactExpires, "tomorrow")}},
		"bind not a thumbprint": {facts: []decodedFact{fact(biscuitFactBind, "key")}},
		"limit not positive":    {facts: []decodedFact{fact(biscuitFactMaxRequests, int64(0))}},
		"budget not an integer": {facts: []decodedFact{fact(biscuitFactMaxBudget, "1")}},
		"unknown fact":          {facts: []decodedFact{fact("paladin_admin", "yes")}},
	}
	for name, blk := range refused {
		if _, err := readBlockFacts(blk, ""); err == nil {
			t.Errorf("%s: read, want refused", name)
		}
	}

	if _, err := readBlockFacts(decodedBlock{facts: []decodedFact{fact(biscuitFactBind, jkt)}}, strings.Repeat("B", 43)); err == nil {
		t.Error("a block rebinding a bound token was read, want refused")
	}

	f, err := readBlockFacts(decodedBlock{facts: []decodedFact{
		fact(biscuitFactExpires, late), fact(biscuitFactExpires, early),
		fact(biscuitFactOp, string(OpGet)), fact(biscuitFactOp, string(OpList)),
		fact(biscuitFactMaxRequests, int64(9)), fact(biscuitFactMaxRequests, int64(3)),
		fact(biscuitFactBind, jkt),
	}}, jkt)
	if err != nil {
		t.Fatal(err)
	}
	if !f.expires.Equal(early) || len(f.ops) != 2 || f.ceiling.MaxRequests != 3 || f.bind != jkt {
		t.Errorf("facts = %+v; want the earliest expiry, both ops, the last limit and the restated binding", f)
	}
}

// A copy's budget is nanos; the micros fact Biscuits carried before is no
// longer part of the vocabulary.
func TestReadBlockFactsBudgetIsNanos(t *testing.T) {
	f, err := readBlockFacts(decodedBlock{facts: []decodedFact{{name: biscuitFactMaxBudget, term: int64(1_500)}}}, "")
	if err != nil || f.ceiling.MaxBudget != 1_500 {
		t.Errorf("budget = %s, %v; want 1500 nanos", f.ceiling.MaxBudget, err)
	}
	if _, err := readBlockFacts(decodedBlock{facts: []decodedFact{{name: "paladin_max_budget_micros", term: int64(1_500)}}}, ""); err == nil {
		t.Error("the micros budget fact was read")
	}
}
