package limes

import (
	"context"
	"log/slog"
	"time"
	"uuid"
)

// The keys of the attributes the issuer and the verifier log, each spelled
// here once. No token, no key and no proof is ever logged: a capability is
// named by its id.
const (
	logKeyOperation  = "op"
	logKeyCapability = "capability"
	logKeyParent     = "parent"
	logKeyIssuer     = "issuer"
	logKeyKeyID      = "kid"
	logKeySubject    = "subject"
	logKeyTenant     = "tenant"
	logKeyClient     = "client"
	logKeyExpiresAt  = "expires_at"
	logKeyBiscuit    = "biscuit"
	logKeyCode       = "code"
	logKeyReason     = "reason"
	logKeyError      = "error"
	logKeyKind       = "kind"
	logKeyAge        = "age"
)

// IssuerOperation names what an Issuer was asked to do, in its log lines
// and its events (IssuerEvents). The values are never renamed; new ones may
// be added.
type IssuerOperation string

// The issuer's operations, each its method.
const (
	IssuerOperationIssue    IssuerOperation = "issue"
	IssuerOperationDelegate IssuerOperation = "delegate"
	IssuerOperationReissue  IssuerOperation = "reissue"
	IssuerOperationBiscuit  IssuerOperation = "biscuit"
)

// VerifierOperation names what a Verifier was asked to do, in its log lines
// and its events (VerifierEvents). The values are never renamed; new ones
// may be added.
type VerifierOperation string

// The verifier's operations, each its method; Authorize is its own.
const (
	VerifierOperationVerify      VerifierOperation = "verify"
	VerifierOperationAuthorize   VerifierOperation = "authorize"
	VerifierOperationIntrospect  VerifierOperation = "introspect"
	VerifierOperationBiscuitCopy VerifierOperation = "biscuit_copy"
)

// IssuerEvents are the callbacks an Issuer makes as it works
// (IssuerConfig.Events), for metrics or an audit sink beside its log. Each is
// optional. They run synchronously, on the caller's goroutine, after the
// outcome is logged and before the method returns: a callback must not
// block, and must not panic — the issuer does not recover one.
type IssuerEvents struct {
	// OnIssued is called for every token minted: by Issue, Delegate,
	// Reissue and Biscuit, which IssuedEvent.Operation tells apart.
	OnIssued func(context.Context, IssuedEvent)
	// OnRefused is called for every request the issuer refused: one
	// malformed, too wide, naming what is not on record, or deriving from a
	// capability expired or revoked.
	OnRefused func(context.Context, IssuerErrorEvent)
	// OnFailed is called for every call that failed for the server's own
	// reasons: a store or a signer that failed, or a feature not configured.
	OnFailed func(context.Context, IssuerErrorEvent)
}

// IssuedEvent is one token an Issuer minted.
type IssuedEvent struct {
	// Operation is the method that minted it.
	Operation IssuerOperation
	// Capability is the capability the token carries: a copy, the
	// callback's own.
	Capability Capability
}

// IssuerErrorEvent is one call an Issuer refused, or that failed.
type IssuerErrorEvent struct {
	// Operation is the method called.
	Operation IssuerOperation
	// CapabilityID is the capability the call named or derived from, when
	// it named one: the parent of a delegation, the capability reissued or
	// sealed. uuid.Nil() for Issue.
	CapabilityID uuid.UUID
	// Code is the error's code (CodeOf); empty for an error that is none of
	// limes's, such as a store's own failure.
	Code Code
	// Err is the error the call returned.
	Err error
}

// VerifierEvents are the callbacks a Verifier makes as it checks tokens
// (VerifierConfig.Events), for metrics or an audit sink beside its log. Each
// is optional. They run synchronously, on the caller's goroutine — every
// request the verifier checks waits for them — after the outcome is logged
// and before the method returns: a callback must not block, and must not
// panic — the verifier does not recover one.
type VerifierEvents struct {
	// OnVerified is called for every token the verifier accepted: by
	// Verify, Authorize, Introspect and BiscuitCopy.
	OnVerified func(context.Context, VerifiedEvent)
	// OnRefused is called for every token the verifier refused, whatever
	// the reason: the reason is in the event.
	OnRefused func(context.Context, VerifierErrorEvent)
	// OnFailed is called for every check that failed for the server's own
	// reasons: the keys or the revocations out of reach, a replay cache that
	// failed, a feature not configured.
	OnFailed func(context.Context, VerifierErrorEvent)
}

// VerifiedEvent is one token a Verifier accepted.
type VerifiedEvent struct {
	// Operation is the method that accepted it.
	Operation VerifierOperation
	// CapabilityID, Issuer, TenantID and Subject name the capability the
	// token carries.
	CapabilityID uuid.UUID
	Issuer       string
	TenantID     uuid.UUID
	Subject      string
	// FromBiscuit reports that the token is a Biscuit.
	FromBiscuit bool
}

// VerifierErrorEvent is one token a Verifier refused, or a check that
// failed.
type VerifierErrorEvent struct {
	// Operation is the method called.
	Operation VerifierOperation
	// Reason is the refusal's reason (ReasonOf); empty for an error that
	// has none.
	Reason Reason
	// Code is the error's code (CodeOf); empty for an error that is none of
	// limes's.
	Code Code
	// Authenticated reports that the token's signature verified before it
	// was refused, so CapabilityID, Issuer and TenantID name a capability
	// its issuer minted; otherwise they are zero.
	Authenticated bool
	CapabilityID  uuid.UUID
	Issuer        string
	TenantID      uuid.UUID
	// Err is the error the call returned.
	Err error
}

// isFailure reports whether err is the server's failure rather than a
// refusal of the request: an error that is none of limes's — a store's own,
// say — or one in CategoryInternal or CategoryUnavailable. Such an error is
// logged at ERROR, a refusal at WARN or below.
func isFailure(err error) bool {
	category, ok := CategoryOf(err)
	return !ok || category == CategoryInternal || category == CategoryUnavailable
}

// orDefault is logger, or slog.Default() for none, resolved at each call so
// a default set after construction is the one logged to.
func orDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// errorAttrs are the attributes that name err: its code and reason when it
// has them, and its text.
func errorAttrs(err error) []slog.Attr {
	var attrs []slog.Attr
	if code, ok := CodeOf(err); ok {
		attrs = append(attrs, slog.String(logKeyCode, string(code)))
	}
	if reason, ok := ReasonOf(err); ok {
		attrs = append(attrs, slog.String(logKeyReason, string(reason)))
	}
	return append(attrs, slog.Any(logKeyError, err))
}

// timeAttr is t under key, in UTC.
func timeAttr(key string, t time.Time) slog.Attr { return slog.Time(key, t.UTC()) }
