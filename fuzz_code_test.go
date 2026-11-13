package limes

import (
	"errors"
	"testing"
)

// noFamily is the index in fuzzFamilies of the error in no family.
const noFamily = 0

// fuzzFamilies are the families an error the fuzzer writes may belong to,
// beside the code it names: none of limes's, and some of each kind.
var fuzzFamilies = []error{errors.New("no family of limes's"), ErrInvalidToken, ErrNotFound, ErrBudgetExceeded, ErrInvalidRequest}

// FuzzProblemOfANamedCode hands ProblemFor an error naming whatever code
// the fuzzer writes, in a family it picks, and holds it to what a consumer
// writes to the wire: a status, a code and a title, always; the named code
// when a table holds it, and never one no table holds; and, for an error in
// no family of limes's, the problem ProblemOfCode gives the code.
func FuzzProblemOfANamedCode(f *testing.F) {
	for _, c := range Codes() {
		f.Add(string(c), uint8(0))
	}
	f.Add("no_such_code", uint8(1))
	f.Add("", uint8(2))
	f.Fuzz(func(t *testing.T, code string, family uint8) {
		i := int(family) % len(fuzzFamilies)
		err := coded{Code(code), fuzzFamilies[i]}
		p := ProblemFor(err)
		if p.Status == 0 || p.Code == "" || p.Title == "" {
			t.Fatalf("an error naming %q: ProblemFor = %+v, no status, code or title", code, p)
		}
		want, held := ProblemOfCode(Code(code))
		switch {
		case !held && p.Code == Code(code):
			t.Fatalf("an error naming %q, a code no table holds, answered with it", code)
		case held && p.Code != Code(code):
			t.Fatalf("an error naming %q, a code a table holds, answered as %q", code, p.Code)
		case held && i == noFamily && (p.Status != want.Status || p.Type != want.Type):
			t.Fatalf("an error naming %q in no family: ProblemFor = %+v, want %+v", code, p, want)
		}
	})
}
