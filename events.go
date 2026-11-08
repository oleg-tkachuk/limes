package limes

import (
	"context"
	"log/slog"
	"time"
	"uuid"
)

// The keys of the attributes the issuer, the verifier and the revoker log,
// each spelled here once. No token, no key and no proof is ever logged: a capability is
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

	// The revoker's: who revoked and why, what a revoke found and wrote,
	// the selectors of a bulk revoke, and a cache cleared after a store
	// error.
	logKeyActor          = "actor"
	logKeyNote           = "note"
	logKeyCascade        = "cascade"
	logKeyAlreadyRevoked = "already_revoked"
	logKeyPrior          = "prior"
	logKeyRevoked        = "revoked"
	logKeyTenants        = "tenants"
	logKeyMore           = "more"
	logKeyAllTenants     = "all_tenants"
	logKeyOnBehalfOf     = "on_behalf_of"
	logKeySession        = "session"
	logKeyRun            = "run"
	logKeySubjectIssuer  = "subject_issuer"
	logKeyDuration       = "duration"
	logKeyCacheCleared   = "cache_cleared"
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

// RevokerOperation names what a Revoker was asked to do, in its log lines
// and its events (RevokerEvents). The values are never renamed; new ones may
// be added.
type RevokerOperation string

// The revoker's operations, each its method.
const (
	RevokerOperationRevoke         RevokerOperation = "revoke"
	RevokerOperationRevokeMatching RevokerOperation = "revoke_matching"
	RevokerOperationRevokeBiscuit  RevokerOperation = "revoke_biscuit"
)

// IssuerEvents are the callbacks an Issuer makes as it works
// (IssuerConfig.Events), for metrics or an audit sink beside its log. Each is
// optional. They run synchronously, on the caller's goroutine, after the
// outcome is logged and before the method returns: a callback must not
// block, and must not panic — the issuer does not recover one.
//
// The callbacks are independent: each outcome calls one of them, and a nil
// one drops its events without a word. An audit sink that must see every
// call sets OnIssued, OnRefused and OnFailed all. ADR-0021 records the
// design.
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
	// Duration is the call's elapsed time, from its entry to the outcome,
	// on the monotonic clock.
	Duration time.Duration
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
	// Duration is the call's elapsed time, from its entry to the outcome,
	// on the monotonic clock.
	Duration time.Duration
}

// VerifierEvents are the callbacks a Verifier makes as it checks tokens
// (VerifierConfig.Events), for metrics or an audit sink beside its log. Each
// is optional. They run synchronously, on the caller's goroutine — every
// request the verifier checks waits for them — after the outcome is logged
// and before the method returns: a callback must not block, and must not
// panic — the verifier does not recover one.
//
// The callbacks are independent: each outcome calls one of them, and a nil
// one drops its events without a word. An audit sink that must see every
// check sets OnVerified, OnRefused and OnFailed all. ADR-0021 records the
// design.
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
	// Duration is the call's elapsed time, from its entry to the outcome,
	// on the monotonic clock.
	Duration time.Duration
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
	// Duration is the call's elapsed time, from its entry to the outcome,
	// on the monotonic clock.
	Duration time.Duration
}

// RevokerEvents are the callbacks a Revoker makes as it revokes
// (RevokerConfig.Events), for metrics or an audit sink beside its log. Each
// is optional. They run synchronously, on the caller's goroutine, after the
// outcome is logged and before the method returns: a callback must not
// block, and must not panic — the revoker does not recover one.
type RevokerEvents struct {
	// OnRevoked is called for every call the store carried out: by Revoke,
	// RevokeMatching and RevokeBiscuit, which RevokedEvent.Operation tells
	// apart — a repeat that wrote nothing among them.
	OnRevoked func(context.Context, RevokedEvent)
	// OnRefused is called for every request the store refused: one
	// malformed, or naming what is not on record or not visible.
	OnRefused func(context.Context, RevokerErrorEvent)
	// OnFailed is called for every call that failed for the server's own
	// reasons: a store that failed, or a feature not configured.
	OnFailed func(context.Context, RevokerErrorEvent)
}

// RevokedEvent is one call a Revoker carried out.
type RevokedEvent struct {
	// Operation is the method called.
	Operation RevokerOperation
	// CapabilityID is the capability Revoke revoked, or whose Biscuit copy
	// RevokeBiscuit revoked; uuid.Nil() for RevokeMatching.
	CapabilityID uuid.UUID
	// TenantID is the tenant RevokeMatching was scoped to; uuid.Nil() for
	// one across every tenant, and for the other operations.
	TenantID uuid.UUID
	// Actor and Note are the request's, as stored for audit.
	Actor, Note string
	// AlreadyRevoked reports, for Revoke and RevokeBiscuit, that the
	// capability or the copy had an entry before the call, which wrote none.
	AlreadyRevoked bool
	// PriorDisposition is Revoke's RevokeResult.PriorDisposition.
	PriorDisposition RecordDisposition
	// Revoked, Tenants and More are RevokeMatching's RevokeMatchingResult:
	// the entries it wrote, the tenants it wrote them in — a copy, the
	// callback's own — and whether more may match.
	Revoked int64
	Tenants []uuid.UUID
	More    bool
	// Duration is the call's elapsed time, from its entry to the outcome,
	// on the monotonic clock.
	Duration time.Duration
}

// RevokerErrorEvent is one call a Revoker's store refused, or that failed.
type RevokerErrorEvent struct {
	// Operation is the method called.
	Operation RevokerOperation
	// CapabilityID is the capability the call named; uuid.Nil() for
	// RevokeMatching.
	CapabilityID uuid.UUID
	// TenantID is the tenant RevokeMatching named; uuid.Nil() otherwise.
	TenantID uuid.UUID
	// Code is the error's code (CodeOf); empty for an error that is none of
	// limes's, such as a store's own failure.
	Code Code
	// Err is the error the call returned.
	Err error
	// Duration is the call's elapsed time, from its entry to the outcome,
	// on the monotonic clock.
	Duration time.Duration
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
