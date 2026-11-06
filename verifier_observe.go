package limes

import (
	"context"
	"log/slog"
	"time"
	"uuid"
)

// Messages of the verifier's log lines.
const (
	msgVerified        = "limes: verifier accepted a token"
	msgVerifierRefused = "limes: verifier refused a token"
	msgVerifierFailed  = "limes: verifier check failed"
)

// observe logs a call to op that began at start and calls the event it
// makes (VerifierEvents): accepted when err is nil, refused or failed
// otherwise. cap is the grant once the token's signature verified, nil
// before; fromBiscuit, that the accepted token is a Biscuit. The levels are
// VerifierConfig.Logger's. With no event set and the line's level off, it
// allocates nothing.
func (v *Verifier) observe(ctx context.Context, op VerifierOperation, start time.Time, cap *grant, fromBiscuit bool, err error) {
	elapsed := time.Since(start)
	if err == nil {
		v.accepted(ctx, op, elapsed, cap, fromBiscuit)
		return
	}
	failed := isFailure(err)
	level, msg, hook := slog.LevelDebug, msgVerifierRefused, v.cfg.Events.OnRefused
	switch {
	case failed:
		level, msg, hook = slog.LevelError, msgVerifierFailed, v.cfg.Events.OnFailed
	case cap != nil:
		level = slog.LevelWarn
	}
	if logger := v.logger(); logger.Enabled(ctx, level) {
		attrs := append(grantAttrs(op, cap), errorAttrs(err)...)
		attrs = append(attrs, slog.Duration(logKeyDuration, elapsed))
		logger.LogAttrs(ctx, level, msg, attrs...)
	}
	if hook == nil {
		return
	}
	ev := VerifierErrorEvent{Operation: op, Err: err, Duration: elapsed}
	ev.Reason, _ = ReasonOf(err)
	ev.Code, _ = CodeOf(err)
	if cap != nil {
		ev.Authenticated, ev.CapabilityID, ev.Issuer, ev.TenantID = true, cap.ID, cap.Issuer, cap.Subject.TenantID
	}
	hook(ctx, ev)
}

// accepted logs, at DEBUG, a token op accepted after elapsed, and calls
// OnVerified.
func (v *Verifier) accepted(ctx context.Context, op VerifierOperation, elapsed time.Duration, cap *grant, fromBiscuit bool) {
	if logger := v.logger(); logger.Enabled(ctx, slog.LevelDebug) {
		attrs := append(grantAttrs(op, cap), slog.Bool(logKeyBiscuit, fromBiscuit), slog.Duration(logKeyDuration, elapsed))
		logger.LogAttrs(ctx, slog.LevelDebug, msgVerified, attrs...)
	}
	if v.cfg.Events.OnVerified != nil {
		v.cfg.Events.OnVerified(ctx, VerifiedEvent{
			Operation: op, CapabilityID: cap.ID, Issuer: cap.Issuer,
			TenantID: cap.Subject.TenantID, Subject: cap.Subject.Subject, FromBiscuit: fromBiscuit,
			Duration: elapsed,
		})
	}
}

// grantAttrs are the attributes naming op and, when its signature verified,
// the capability a token carries: its id, issuer, kid, tenant and subject.
func grantAttrs(op VerifierOperation, cap *grant) []slog.Attr {
	attrs := []slog.Attr{slog.String(logKeyOperation, string(op))}
	if cap == nil {
		return attrs
	}
	attrs = append(attrs,
		slog.String(logKeyCapability, cap.ID.String()),
		slog.String(logKeyIssuer, cap.Issuer),
		slog.String(logKeyKeyID, cap.kid),
		slog.String(logKeyTenant, cap.Subject.TenantID.String()),
		slog.String(logKeySubject, cap.Subject.Subject))
	if cap.ParentID != uuid.Nil() {
		attrs = append(attrs, slog.String(logKeyParent, cap.ParentID.String()))
	}
	return attrs
}
