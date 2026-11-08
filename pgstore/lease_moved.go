package pgstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// leaseMovedAttempts bounds how often a call runs whose lease moved between
// its read and its lock (errLeaseMoved). A top-up settles a lease and opens
// another, so a charge waiting on a busy leaseholder may meet more than one
// move; past the bound it gives up rather than run for as long as top-ups
// keep coming.
const leaseMovedAttempts = 5

// errLeaseKeptMoving is a call that gave up once its lease had moved
// leaseMovedAttempts times: the lease on record kept changing under it, a
// conflict the caller may retry.
var errLeaseKeptMoving = fmt.Errorf("%w: pgstore: a lease kept moving while the call locked it", limes.ErrConflict)

// retryLeaseMoved runs attempt, a call's whole transaction, again for as
// long as it reports errLeaseMoved, at most leaseMovedAttempts times in
// all, and logs a call that gave up, for op on capID.
func (s *UsageStore) retryLeaseMoved(ctx context.Context, op string, capID uuid.UUID, attempt func() error) error {
	for range leaseMovedAttempts {
		if err := attempt(); !errors.Is(err, errLeaseMoved) {
			return err
		}
	}
	s.log.WarnContext(ctx, "pgstore: gave up on a call whose lease kept moving while it locked it",
		slog.String(logKeyOp, op), slog.String(logKeyCapability, capID.String()), slog.Int(logKeyAttempts, leaseMovedAttempts))
	return errLeaseKeptMoving
}
