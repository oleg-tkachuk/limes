package exchange

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// updateErrorsJSON rewrites errorsJSON from the code table, as task
// docs:api does.
var updateErrorsJSON = flag.Bool("update", false, "rewrite docs/errors.json from the code table")

// errorsJSON is the machine-readable table of every code, for a consumer
// that cannot call limes's functions: written here, in the one binary that
// links limes's codes, the meter's and the exchange's together.
const errorsJSON = "../docs/errors.json"

// errorsJSONIndent is the file's indentation.
const errorsJSONIndent = "  "

// codeEntry is one code as docs/errors.json lists it: what limes.ProblemOfCode,
// ReasonOfCode and Reason.Permanent answer for it.
type codeEntry struct {
	Code     limes.Code     `json:"code"`
	Status   int            `json:"status"`
	Category limes.Category `json:"category"`
	Title    string         `json:"title"`
	Type     string         `json:"type"`
	// Reason is the refusal's reason, absent for a code that is none.
	Reason limes.Reason `json:"reason,omitempty"`
	// Permanent says the refusal stands for as long as the capability does
	// (limes.Reason.Permanent): false for every code that is no refusal.
	Permanent bool `json:"permanent"`
}

// errorsTable is every code limes.Codes lists, in code order, so a release
// that reorders Codes changes nothing in the file.
func errorsTable(t *testing.T) []byte {
	t.Helper()
	codes := limes.Codes()
	slices.Sort(codes)
	entries := make([]codeEntry, 0, len(codes))
	for _, code := range codes {
		p, ok := limes.ProblemOfCode(code)
		if !ok {
			t.Fatalf("%s: no problem", code)
		}
		e := codeEntry{Code: code, Status: p.Status, Category: p.Category, Title: p.Title, Type: p.Type}
		if reason, ok := limes.ReasonOfCode(code); ok {
			e.Reason, e.Permanent = reason, reason.Permanent()
		}
		entries = append(entries, e)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetIndent("", errorsJSONIndent)
	if err := enc.Encode(entries); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// docs/errors.json is the code table as it is: run task docs:api after a
// code, a status, a category, a title or a reason changes.
func TestErrorsJSONIsCurrent(t *testing.T) {
	want := errorsTable(t)
	if *updateErrorsJSON {
		if err := os.WriteFile(errorsJSON, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(errorsJSON)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the code table (%v): run task docs:api", errorsJSON, err)
	}
}

// Every code the file lists reads back with the answer limes gives it: a
// reader of the file branches as a Go consumer would.
func TestErrorsJSONReadsBack(t *testing.T) {
	var entries []codeEntry
	if err := json.Unmarshal(errorsTable(t), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(limes.Codes()) {
		t.Fatalf("%d entries for %d codes", len(entries), len(limes.Codes()))
	}
	for _, e := range entries {
		p, _ := limes.ProblemOfCode(e.Code)
		if p.Status != e.Status || p.Category != e.Category || p.Title != e.Title {
			t.Errorf("%s: %+v, ProblemOfCode %+v", e.Code, e, p)
		}
		if reason, ok := limes.ReasonOfCode(e.Code); ok != (e.Reason != "") || ok && reason.Permanent() != e.Permanent {
			t.Errorf("%s: reason %q permanent %v, ReasonOfCode %q", e.Code, e.Reason, e.Permanent, reason)
		}
	}
}
