package pgstore

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// settledThen settles and acts again for each lease opened in between, up
// to settleAttempts, saying so at each retry; past them it refuses the call
// as ErrConflict and logs why.
func TestSettledThenRetriesUpToItsAttempts(t *testing.T) {
	errAct := errors.New("act failed")
	cases := []struct {
		name string
		// opened is how many runs of act find a lease opened before it
		// returns last.
		opened   int
		last     error
		want     error
		attempts int
		warns    int
		errs     int
	}{
		{name: "no lease opened", attempts: 1},
		{name: "one lease opened", opened: 1, attempts: 2, warns: 1},
		{name: "leases opened short of the bound", opened: settleAttempts - 1, attempts: settleAttempts, warns: settleAttempts - 1},
		{name: "act's own error", last: errAct, want: errAct, attempts: 1},
		{name: "leases kept opening", opened: settleAttempts, want: limes.ErrConflict, attempts: settleAttempts, warns: settleAttempts - 1, errs: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := &logRecords{}
			s := &UsageStore{conn{log: slog.New(logged)}}
			settles, acts := 0, 0
			err := s.settledThen(t.Context(), settledPurgeOrphans,
				func() error { settles++; return nil },
				func() error {
					acts++
					if acts <= tc.opened {
						return errLeaseOpened
					}
					return tc.last
				})
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("settledThen = %v, want %v", err, tc.want)
			}
			if settles != tc.attempts || acts != tc.attempts {
				t.Fatalf("settled %d and acted %d times, want %d each", settles, acts, tc.attempts)
			}
			warns, errs := 0, 0
			for _, r := range logged.records {
				if r.Level == slog.LevelWarn {
					warns++
				}
				if r.Level == slog.LevelError {
					errs++
				}
				var op string
				r.Attrs(func(a slog.Attr) bool {
					if a.Key == logKeyOp {
						op = a.Value.String()
					}
					return true
				})
				if op != settledPurgeOrphans {
					t.Errorf("a log line names op %q, want %q", op, settledPurgeOrphans)
				}
			}
			if warns != tc.warns || errs != tc.errs {
				t.Fatalf("logged %d warnings and %d errors, want %d and %d", warns, errs, tc.warns, tc.errs)
			}
		})
	}
}

// A settle that fails ends the call with its error, before act runs.
func TestSettledThenStopsAtAFailedSettle(t *testing.T) {
	errSettle := errors.New("settle failed")
	s := &UsageStore{conn{log: slog.New(&logRecords{})}}
	acted := false
	err := s.settledThen(t.Context(), settledPurgeOrphans,
		func() error { return errSettle },
		func() error { acted = true; return nil })
	if !errors.Is(err, errSettle) || acted {
		t.Fatalf("settledThen = %v, acted %v; want the settle's error and no act", err, acted)
	}
}
