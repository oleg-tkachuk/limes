package pgstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/oleg-tkachuk/limes"
)

// lostRaceAttempts is how often a call runs that lost a race to another
// committing under its locks: once, and once more to answer from what that
// one committed. Losing again is a conflict the caller may retry.
const lostRaceAttempts = 2

// logKeyReservation is the reservation id a log line is about.
const logKeyReservation = "reservation"

// Calls that kept losing their race, as their callers see them: conflicts
// they may retry, never the ErrInternal each attempt rolled back with.
var (
	errHoldKeptTaken  = fmt.Errorf("%w: pgstore: a reservation id kept being taken while the call waited for its locks", limes.ErrConflict)
	errLeaseKeptStale = fmt.Errorf("%w: pgstore: a lease kept needing settling before another could open", limes.ErrConflict)
)

// lostRace is a race a call can lose to another committing under its locks.
type lostRace struct {
	// op is the call, as a log line names it.
	op string
	// lost is what an attempt that lost the race returns.
	lost error
	// gaveUp is what the call returns once it lost every attempt.
	gaveUp error
	// about are the attributes naming what the call was about.
	about []any
}

// retryLostRace runs attempt, a call's whole transaction, again for as long
// as it loses race, at most lostRaceAttempts times in all, running between
// before each run after the first; a call that lost every time is logged
// with what it was about, and refused with race.gaveUp.
func (s *UsageStore) retryLostRace(ctx context.Context, race lostRace, attempt, between func() error) error {
	for n := 1; ; n++ {
		err := attempt()
		if !errors.Is(err, race.lost) {
			return err
		}
		if n == lostRaceAttempts {
			attrs := append([]any{slog.String(logKeyOp, race.op), slog.Int(logKeyAttempts, n), slog.Any(logKeyError, err)}, race.about...)
			s.log.WarnContext(ctx, "pgstore: gave up on a call that kept losing a race to another committing under its locks", attrs...)
			return race.gaveUp
		}
		if err := between(); err != nil {
			return err
		}
	}
}

// noneBetween is retryLostRace's between for a call that only runs again.
func noneBetween() error { return nil }
