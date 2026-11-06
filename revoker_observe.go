package limes

import (
	"context"
	"log/slog"
	"time"
)

// Messages of the revoker's log lines.
const (
	msgRevoked        = "limes: revoker revoked"
	msgRevokerRefused = "limes: revoker refused a request"
	msgRevokerFailed  = "limes: revoker call failed"
)

// log is RevokerConfig.Logger, or slog.Default() for none.
func (r *Revoker) log() *slog.Logger { return orDefault(r.logger) }

// revoked logs, at INFO, a call that began at start and the store carried
// out, under attrs, and calls OnRevoked.
func (r *Revoker) revoked(ctx context.Context, start time.Time, ev RevokedEvent, attrs []slog.Attr) {
	ev.Duration = time.Since(start)
	attrs = append([]slog.Attr{slog.String(logKeyOperation, string(ev.Operation))}, attrs...)
	attrs = append(attrs, slog.Duration(logKeyDuration, ev.Duration))
	r.log().LogAttrs(ctx, slog.LevelInfo, msgRevoked, attrs...)
	if r.events.OnRevoked != nil {
		r.events.OnRevoked(ctx, ev)
	}
}

// refused logs a call that began at start and returned ev.Err, under attrs,
// at WARN for a refusal and at ERROR for a failure (isFailure), and calls
// OnRefused or OnFailed with ev and the error's code.
func (r *Revoker) refused(ctx context.Context, start time.Time, ev RevokerErrorEvent, attrs []slog.Attr) {
	ev.Duration = time.Since(start)
	level, msg, hook := slog.LevelWarn, msgRevokerRefused, r.events.OnRefused
	if isFailure(ev.Err) {
		level, msg, hook = slog.LevelError, msgRevokerFailed, r.events.OnFailed
	}
	all := append([]slog.Attr{slog.String(logKeyOperation, string(ev.Operation))}, attrs...)
	all = append(all, errorAttrs(ev.Err)...)
	all = append(all, slog.Duration(logKeyDuration, ev.Duration))
	r.log().LogAttrs(ctx, level, msg, all...)
	if hook != nil {
		ev.Code, _ = CodeOf(ev.Err)
		hook(ctx, ev)
	}
}
