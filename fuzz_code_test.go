package limes

import (
	"errors"
	"testing"
)

// fuzzFamilies are the families an error the fuzzer writes may belong to,
// beside the code it names: none of limes's, and some of each kind.
var fuzzFamilies = []error{errors.New("no family of limes's"), ErrInvalidToken, ErrNotFound, ErrBudgetExceeded, ErrInvalidRequest}

// FuzzProblemOfANamedCode hands ProblemFor an error naming whatever code
// the fuzzer writes, in a family it picks, and holds it to what a consumer
// writes to the wire: a status, a code and a title, always; never a code no
// table holds; and for a code a table holds, the problem ProblemOfCode
// gives it, whatever family the error is in, so its type, reason and status
// are the code's own.
func FuzzProblemOfANamedCode(f *testing.F) {
	for _, c := range Codes() {
		f.Add(string(c), uint8(0))
	}
	f.Add("no_such_code", uint8(1))
	f.Add("", uint8(2))
	f.Fuzz(func(t *testing.T, code string, family uint8) {
		err := coded{Code(code), fuzzFamilies[int(family)%len(fuzzFamilies)]}
		p := ProblemFor(err)
		if p.Status == 0 || p.Code == "" || p.Title == "" {
			t.Fatalf("an error naming %q: ProblemFor = %+v, no status, code or title", code, p)
		}
		want, held := ProblemOfCode(Code(code))
		switch {
		case !held && p.Code == Code(code):
			t.Fatalf("an error naming %q, a code no table holds, answered with it", code)
		case held && (p.Code != want.Code || p.Status != want.Status || p.Type != want.Type || p.Reason != want.Reason || p.Category != want.Category):
			t.Fatalf("an error naming %q: ProblemFor = %+v, want %+v", code, p, want)
		}
	})
}
