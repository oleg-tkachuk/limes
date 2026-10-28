package expect

import (
	"fmt"
	"testing"
)

// recorder is a Reporter that keeps what it was told.
type recorder struct {
	helpers int
	errors  []string
	fatals  []string
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func (r *recorder) Helper() { r.helpers++ }

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// That reports only a failed expectation, with its message, and marks
// itself a helper so the report names the caller's line.
func TestThat(t *testing.T) {
	for name, tc := range map[string]struct {
		ok   bool
		want []string
	}{
		"met":    {true, nil},
		"failed": {false, []string{"count = 2, want 3"}},
	} {
		var r recorder
		That(&r, tc.ok, "count = %d, want %d", 2, 3)
		if fmt.Sprint(r.errors) != fmt.Sprint(tc.want) || r.helpers != 1 {
			t.Errorf("%s: errors %q, helpers %d; want %q, 1", name, r.errors, r.helpers, tc.want)
		}
	}
}

// Must stops only on a failed expectation, with its message, and marks
// itself a helper so the report names the caller's line.
func TestMust(t *testing.T) {
	for name, tc := range map[string]struct {
		ok   bool
		want []string
	}{
		"met":    {true, nil},
		"failed": {false, []string{"setup: refused"}},
	} {
		var r recorder
		Must(&r, tc.ok, "setup: %s", "refused")
		if fmt.Sprint(r.fatals) != fmt.Sprint(tc.want) || r.errors != nil || r.helpers != 1 {
			t.Errorf("%s: fatals %q, errors %q, helpers %d; want %q, none, 1", name, r.fatals, r.errors, r.helpers, tc.want)
		}
	}
}
