package pgstore

import (
	"errors"
	"log/slog"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// A call that loses its race once runs again, after between, and answers as
// that run did; one that loses every time gives up after lostRaceAttempts
// with ErrConflict — never the ErrInternal its attempts rolled back with —
// and says so at WARN with the call and the ids it was about. Any other
// error, and one between returns, ends it at once.
func TestRetryLostRaceIsBoundedAndLogged(t *testing.T) {
	errOther := errors.New("another failure")
	races := []struct {
		name   string
		op     string
		lost   error
		gaveUp error
	}{
		{name: "reservation id taken", op: retriedReserve, lost: errHoldTakenAfterLock, gaveUp: errHoldKeptTaken},
		{name: "lease to settle first", op: retriedLease, lost: errLeaseStale, gaveUp: errLeaseKeptStale},
	}
	cases := []struct {
		name string
		// losses is how many runs lose before one does not.
		losses      int
		last        error
		betweenErr  error
		want        error
		wantRuns    int
		wantBetween int
		warned      bool
	}{
		{name: "won at once", losses: 0, wantRuns: 1},
		{name: "lost once", losses: lostRaceAttempts - 1, wantRuns: lostRaceAttempts, wantBetween: lostRaceAttempts - 1},
		{name: "lost once, then failed", losses: 1, last: errOther, want: errOther, wantRuns: 2, wantBetween: 1},
		{name: "between failed", losses: 1, betweenErr: errOther, want: errOther, wantRuns: 1, wantBetween: 1},
		{name: "lost every time", losses: lostRaceAttempts, want: limes.ErrConflict, wantRuns: lostRaceAttempts, wantBetween: lostRaceAttempts - 1, warned: true},
	}
	for _, race := range races {
		for _, tc := range cases {
			t.Run(race.name+"/"+tc.name, func(t *testing.T) {
				logged := &logRecords{}
				s := &UsageStore{log: slog.New(logged)}
				capID := uuid.New()
				runs, betweens := 0, 0
				err := s.retryLostRace(t.Context(), lostRace{
					op: race.op, lost: race.lost, gaveUp: race.gaveUp,
					about: []any{slog.String(logKeyCapability, capID.String())},
				}, func() error {
					runs++
					if runs <= tc.losses {
						return race.lost
					}
					return tc.last
				}, func() error {
					betweens++
					return tc.betweenErr
				})
				if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
					t.Errorf("retryLostRace = %v, want %v", err, tc.want)
				}
				if errors.Is(err, limes.ErrInternal) {
					t.Errorf("retryLostRace handed an ErrInternal to the caller: %v", err)
				}
				if runs != tc.wantRuns || betweens != tc.wantBetween {
					t.Errorf("ran %d times with %d between, want %d and %d", runs, betweens, tc.wantRuns, tc.wantBetween)
				}
				var warned bool
				for _, r := range logged.records {
					attrs := map[string]slog.Value{}
					r.Attrs(func(a slog.Attr) bool {
						attrs[a.Key] = a.Value
						return true
					})
					if r.Level == slog.LevelWarn && attrs[logKeyOp].String() == race.op &&
						attrs[logKeyCapability].String() == capID.String() && attrs[logKeyAttempts].Int64() == lostRaceAttempts {
						warned = true
					}
				}
				if warned != tc.warned {
					t.Errorf("warned = %t, want %t; logged %d lines", warned, tc.warned, len(logged.records))
				}
			})
		}
	}
}
