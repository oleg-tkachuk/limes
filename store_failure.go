package limes

import "errors"

// A store's own failures, which a store wraps its driver's error in so that
// a caller answers each by its code rather than as an error of its own: the
// driver's error stays in the chain, for the log and for errors.As.
var (
	// ErrStoreUnavailable is a store that could not serve the call and may
	// serve it again: no connection to it, one it dropped before its answer
	// to a call that wrote nothing, the database shutting down or starting,
	// out of resources, or a transaction it aborted over a conflict another
	// one won. A call that wrote and whose commit went unanswered is never
	// this: its outcome is unknown (meter.ErrOutcomeUnknown), and sending it
	// again could apply it twice. Answered 503, to retry.
	ErrStoreUnavailable = errors.New("limes: store unavailable")
	// ErrStoreTimeout is a statement the store cancelled past its own
	// limit — PostgreSQL's statement_timeout or lock_timeout — the caller's
	// context still live. It wrote nothing. Answered 504, to retry.
	ErrStoreTimeout = errors.New("limes: store timed out")
)
