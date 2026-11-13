package pgstore

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/errs"
)

// logKeyCode and logKeySQLState name a classified failure's code and the
// database's SQLSTATE in its log line.
const (
	logKeyCode     = "code"
	logKeySQLState = "sqlstate"
)

// reportFailure is storeFailure of err, logged at WARN when it gave err a
// code: the store decided the caller may send the call again, and the line
// says why.
func reportFailure(ctx context.Context, log *slog.Logger, err error, writes bool) error {
	classified, ok := storeFailure(ctx, err, writes)
	if !ok {
		return err
	}
	code, _ := limes.CodeOf(classified)
	attrs := []slog.Attr{slog.String(logKeyCode, string(code)), slog.Any(logKeyError, err)}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		attrs = append(attrs, slog.String(logKeySQLState, pgErr.Code))
	}
	log.LogAttrs(ctx, slog.LevelWarn, "pgstore: the database could not serve a call; answered with a code to retry", attrs...)
	return classified
}

// storeFailure is err, from a call's statements, as the caller answers it:
// wrapped in limes.ErrStoreUnavailable or limes.ErrStoreTimeout when the
// database could not serve the call and may serve it sent again, and as it
// is otherwise — the driver's error stays in the chain either way — and
// whether it added a code. writes says the call wrote.
//
// It leaves alone an error that has a code of limes's already —
// meter.ErrOutcomeUnknown, a refusal — and one the caller's context
// explains, its deadline or its cancelling being the caller's own. In a call
// that wrote it classifies only what says the writes did not land: the
// database's own answer (PgError), or pgx sending nothing; a connection lost
// after bytes left may have carried COMMIT, and sending such a call again
// could apply it twice.
func storeFailure(ctx context.Context, err error, writes bool) (error, bool) {
	if err == nil || ctx.Err() != nil {
		return err, false
	}
	if _, ok := limes.CodeOf(err); ok {
		return err, false
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch {
		case timedOut(pgErr.Code):
			return errs.Errorf("%w: %w", limes.ErrStoreTimeout, err), true
		case unavailable(pgErr.Code):
			return errs.Errorf("%w: %w", limes.ErrStoreUnavailable, err), true
		}
		return err, false
	}
	if notConnected(err) || pgconn.SafeToRetry(err) || !writes && lostConnection(err) {
		return errs.Errorf("%w: %w", limes.ErrStoreUnavailable, err), true
	}
	return err, false
}

// timedOut reports whether code is a statement the database cancelled past
// a limit of its own: statement_timeout, lock_timeout, or
// idle_in_transaction_session_timeout. The caller's own cancelling is told
// apart by its context (storeFailure).
func timedOut(code string) bool {
	switch code {
	case pgerrcode.QueryCanceled, pgerrcode.LockNotAvailable, pgerrcode.IdleInTransactionSessionTimeout:
		return true
	}
	return false
}

// unavailable reports whether code is the database unable to serve the call
// for now: the connection's failure (class 08), the server shutting down or
// starting (57P01–57P03), out of resources (class 53), or a transaction
// aborted over a conflict another one won (40001). A COMMIT whose
// completion is unknown (40003) is not among them, nor a deadlock (40P01):
// a meter call runs again once deadlocked, and retryDeadlock answers the
// second.
func unavailable(code string) bool {
	switch code {
	case pgerrcode.AdminShutdown, pgerrcode.CrashShutdown, pgerrcode.CannotConnectNow, pgerrcode.SerializationFailure:
		return true
	}
	return pgerrcode.IsConnectionException(code) || pgerrcode.IsInsufficientResources(code)
}

// notConnected reports whether err is a connection that was never made:
// nothing reached the database.
func notConnected(err error) bool {
	var connectErr *pgconn.ConnectError
	return errors.As(err, &connectErr)
}

// lostConnection reports whether err is the connection failing under a
// call: closed, cut off on the network, or timed out there.
func lostConnection(err error) bool {
	var netErr net.Error
	return errors.Is(err, pgconn.ErrConnClosed) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &netErr) || pgconn.Timeout(err)
}
