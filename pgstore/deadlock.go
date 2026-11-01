package pgstore

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
)

// sqlDeadlockDetected is PostgreSQL's code for a transaction it aborted to
// break a cycle of waits.
const sqlDeadlockDetected = "40P01"

// isDeadlock reports whether err is PostgreSQL breaking a deadlock.
func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == sqlDeadlockDetected
}

// logKeyOp names the meter call a log line is about, one of the op names
// below.
const logKeyOp = "op"

// logKeyAttempts is how many times a call has run, as a log line counts.
const logKeyAttempts = "attempts"

// The meter calls retryDeadlock retries, as a log line names them.
const (
	retriedBump        = "Bump"
	retriedCharge      = "Charge"
	retriedReserve     = "Reserve"
	retriedSettle      = "Settle"
	retriedRelease     = "Release"
	retriedRefund      = "Refund"
	retriedLease       = "Lease"
	retriedSettleLease = "SettleLease"
)

// retryDeadlock runs call, a meter call's whole transaction, and runs it
// once more when PostgreSQL aborted it to break a deadlock: every call takes
// its locks in one order, so none deadlocks another of this version, but
// during a rolling upgrade one of a version that took them in another order
// can (docs/adr/0018-fewer-round-trips-per-charge.md). The transaction was
// rolled back whole, so running it again — onCharged among it — moves
// nothing twice. A second deadlock is returned.
func (c conn) retryDeadlock(ctx context.Context, op string, call func() error) error {
	err := call()
	if !isDeadlock(err) {
		return err
	}
	c.log.WarnContext(ctx, "pgstore: retrying a meter call PostgreSQL aborted to break a deadlock",
		slog.String(logKeyOp, op), slog.Any("error", err))
	return call()
}
