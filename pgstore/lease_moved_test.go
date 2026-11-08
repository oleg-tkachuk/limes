package pgstore

import (
	"errors"
	"log/slog"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// A call whose lease moves fewer times than the bound runs until it does
// not, and answers as that run did; one whose lease moves every time gives
// up after the bound with ErrConflict, and says so at WARN with the
// capability and the attempts. Any other error ends the retries at once.
func TestRetryLeaseMovedIsBounded(t *testing.T) {
	errOther := errors.New("another failure")
	cases := []struct {
		name string
		// moves is how many runs report errLeaseMoved before one does not.
		moves    int
		last     error
		want     error
		wantRuns int
		warned   bool
	}{
		{name: "no move", moves: 0, wantRuns: 1},
		{name: "moved within the bound", moves: leaseMovedAttempts - 1, wantRuns: leaseMovedAttempts},
		{name: "moved within the bound, then failed", moves: 1, last: errOther, want: errOther, wantRuns: 2},
		{name: "moved on every run", moves: leaseMovedAttempts, want: limes.ErrConflict, wantRuns: leaseMovedAttempts, warned: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := &logRecords{}
			s := &UsageStore{log: slog.New(logged)}
			capID := uuid.New()
			runs := 0
			err := s.retryLeaseMoved(t.Context(), retriedCharge, capID, func() error {
				runs++
				if runs <= tc.moves {
					return errLeaseMoved
				}
				return tc.last
			})
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("retryLeaseMoved = %v, want %v", err, tc.want)
			}
			if errors.Is(err, errLeaseMoved) {
				t.Errorf("retryLeaseMoved handed errLeaseMoved, an ErrInternal, to the caller: %v", err)
			}
			if runs != tc.wantRuns {
				t.Errorf("the call ran %d times, want %d", runs, tc.wantRuns)
			}
			var warned bool
			for _, r := range logged.records {
				attrs := map[string]slog.Value{}
				r.Attrs(func(a slog.Attr) bool {
					attrs[a.Key] = a.Value
					return true
				})
				if r.Level == slog.LevelWarn && attrs[logKeyOp].String() == retriedCharge &&
					attrs[logKeyCapability].String() == capID.String() && attrs[logKeyAttempts].Int64() == leaseMovedAttempts {
					warned = true
				}
			}
			if warned != tc.warned {
				t.Errorf("warned = %t, want %t; logged %d lines", warned, tc.warned, len(logged.records))
			}
		})
	}
}
