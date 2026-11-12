package pgstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes/internal/errs"
	"github.com/oleg-tkachuk/limes/meter"
)

// commitUnanswered reports whether err, from the batch that carried a
// call's COMMIT or from COMMIT itself, leaves the call's outcome unknown:
// COMMIT may have reached the database, and its answer did not come back.
//
// The outcome is known when the database answered: an error of its own
// (PgError) of ERROR severity aborted the transaction, before COMMIT ran or
// as COMMIT failed, or COMMIT answered that it rolled back. A FATAL or PANIC
// one ended the session, which it may have done once COMMIT had committed:
// that is unknown. It is known too when the transaction had already
// ended (ErrTxClosed), or pgx sent nothing (SafeToRetry) — but for a closed
// connection (pgconn.ErrConnClosed): pgx reports a COMMIT whose answer a
// dropped connection lost as that, though COMMIT left, so it is taken as
// unknown. Anything else — the connection failing, the context ending —
// came after bytes left, and the database may have committed them.
func commitUnanswered(err error) bool {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return false
	case errors.As(err, &pgErr):
		return endedSession(pgErr)
	case errors.Is(err, pgx.ErrTxCommitRollback), errors.Is(err, pgx.ErrTxClosed):
		return false
	case errors.Is(err, pgconn.ErrConnClosed):
		return true
	case pgconn.SafeToRetry(err):
		return false
	}
	return true
}

// The severities PostgreSQL names an error that ends the session with, as
// sent untranslated (SeverityUnlocalized), and the one that aborts only the
// transaction.
const (
	severityFatal = "FATAL"
	severityPanic = "PANIC"
	severityError = "ERROR"
)

// endedSession reports whether pgErr ended the session rather than the
// transaction alone. A server older than 9.6 sends the severity only
// translated (Severity), which then names it in English or not at all.
func endedSession(pgErr *pgconn.PgError) bool {
	severity := pgErr.SeverityUnlocalized
	if severity == "" {
		severity = pgErr.Severity
	}
	return severity == severityFatal || severity == severityPanic
}

// outcomeOf is err as meter.ErrOutcomeUnknown, wrapping it, when it left the
// call's commit unanswered (commitUnanswered), and err as it is otherwise.
func outcomeOf(err error) error {
	if !commitUnanswered(err) {
		return err
	}
	return errs.Errorf("%w: %w", meter.ErrOutcomeUnknown, err)
}

// commitOutcome is outcomeOf for err, the error of the batch that carried
// tx's COMMIT (queueCommit): nil once COMMIT has answered that it
// committed, whatever failed after it, since the call's writes stand.
func commitOutcome(tx pgx.Tx, err error) error {
	if d, ok := tx.(*deferTx); ok && d.committed {
		return nil
	}
	return outcomeOf(err)
}

// beginFunc is pgx.BeginTxFunc over db, its COMMIT's error passed through
// commitErr: fn runs in one transaction, rolled back when fn fails and
// committed when it does not.
func beginFunc(ctx context.Context, db txBeginner, fn func(pgx.Tx) error, commitErr func(error) error) (err error) {
	tx, err := db.BeginTx(ctx, txOptions)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = rollbackErr
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx) // the error to return is fn's
		return err
	}
	return commitErr(tx.Commit(ctx))
}
