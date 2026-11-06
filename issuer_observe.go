package limes

import (
	"context"
	"log/slog"
	"uuid"
)

// Messages of the issuer's log lines.
const (
	msgIssued        = "limes: issuer minted a token"
	msgIssuerRefused = "limes: issuer refused a request"
	msgIssuerFailed  = "limes: issuer call failed"
)

// log is IssuerConfig.Logger, or slog.Default() for none.
func (i *Issuer) log() *slog.Logger { return orDefault(i.logger) }

// issued logs, at INFO, a token minted by op for c, and calls OnIssued.
func (i *Issuer) issued(ctx context.Context, op IssuerOperation, c Capability) {
	attrs := []slog.Attr{
		slog.String(logKeyOperation, string(op)),
		slog.String(logKeyCapability, c.ID.String()),
		slog.String(logKeyIssuer, c.Issuer),
		slog.String(logKeySubject, c.Subject.Subject),
		slog.String(logKeyTenant, c.Subject.TenantID.String()),
		slog.String(logKeyClient, c.ClientID),
		timeAttr(logKeyExpiresAt, c.ExpiresAt),
	}
	if c.ParentID != uuid.Nil() {
		attrs = append(attrs, slog.String(logKeyParent, c.ParentID.String()))
	}
	i.log().LogAttrs(ctx, slog.LevelInfo, msgIssued, attrs...)
	if i.events.OnIssued != nil {
		i.events.OnIssued(ctx, IssuedEvent{Operation: op, Capability: c.clone()})
	}
}

// refused logs a call to op that returned err — about the capability id
// when it named one, with extra — at WARN for a refusal and at ERROR for a
// failure (isFailure), and calls OnRefused or OnFailed.
func (i *Issuer) refused(ctx context.Context, op IssuerOperation, id uuid.UUID, err error, extra ...slog.Attr) {
	failed := isFailure(err)
	level, msg, hook := slog.LevelWarn, msgIssuerRefused, i.events.OnRefused
	if failed {
		level, msg, hook = slog.LevelError, msgIssuerFailed, i.events.OnFailed
	}
	attrs := []slog.Attr{slog.String(logKeyOperation, string(op))}
	if id != uuid.Nil() {
		attrs = append(attrs, slog.String(logKeyCapability, id.String()))
	}
	attrs = append(attrs, extra...)
	attrs = append(attrs, errorAttrs(err)...)
	i.log().LogAttrs(ctx, level, msg, attrs...)
	if hook != nil {
		code, _ := CodeOf(err)
		hook(ctx, IssuerErrorEvent{Operation: op, CapabilityID: id, Code: code, Err: err})
	}
}
