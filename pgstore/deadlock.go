package pgstore

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/meter"
)

// isDeadlock reports whether err is PostgreSQL breaking a deadlock.
func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.DeadlockDetected
}

// logKeyOp names the meter call a log line is about, one of the op names
// below.
const logKeyOp = "op"

// logKeyAttempts is how many times a call has run, as a log line counts.
const logKeyAttempts = "attempts"

// The meter calls retryDeadlock retries, as a log line names them.
const (
	retriedBump        = "Bump"
	retriedCharge      = string(meter.StoreOperationCharge)
	retriedReserve     = string(meter.StoreOperationReserve)
	retriedSettle      = string(meter.StoreOperationSettle)
	retriedRelease     = string(meter.StoreOperationRelease)
	retriedRefund      = string(meter.StoreOperationRefund)
	retriedLease       = string(meter.StoreOperationLease)
	retriedSettleLease = string(meter.StoreOperationSettleLease)
)

// logKeyError is the error a log line is about.
const logKeyError = "error"

// retryDeadlock runs call, a meter call's whole transaction, and runs it
// once more when PostgreSQL aborted it to break a deadlock: every call takes
// its locks in one order, so none deadlocks another of this version, but
// during a rolling upgrade one of a version that took them in another order
// can (docs/adr/0018-fewer-round-trips-per-charge.md). The transaction was
// rolled back whole, so running it again — onCharged or onRefunded among
// it — moves
// nothing twice. A second deadlock is returned as limes.ErrStoreUnavailable
// wrapping it: sent again, the call may find the old order gone.
//
// A deadlock a callback returned while the call's transaction stood
// (deadlockOutside) is not run again: it was another transaction's, and
// what the callback did beside the call stands.
//
// A call whose COMMIT went unanswered (meter.ErrOutcomeUnknown) is not run
// again: it may have committed, and only its caller knows whether it is
// keyed. It is logged, as nothing else tells the operator a commit was lost.
func (c conn) retryDeadlock(ctx context.Context, op string, call func() error) error {
	err := call()
	if isDeadlock(err) && !errors.As(err, new(deadlockOutside)) {
		c.log.WarnContext(ctx, "pgstore: retrying a meter call PostgreSQL aborted to break a deadlock",
			slog.String(logKeyOp, op), slog.Any(logKeyError, err))
		err = call()
		if isDeadlock(err) {
			c.log.WarnContext(ctx, "pgstore: a meter call deadlocked again; answered with a code to retry",
				slog.String(logKeyOp, op), slog.String(logKeyCode, string(limes.CodeStoreUnavailable)), slog.Any(logKeyError, err))
			return errs.Errorf("%w: %w", limes.ErrStoreUnavailable, err)
		}
	}
	if errors.Is(err, meter.ErrOutcomeUnknown) {
		c.log.WarnContext(ctx, "pgstore: lost the answer to a meter call's commit; it may have committed",
			slog.String(logKeyOp, op), slog.Any(logKeyError, err))
	}
	return err
}
