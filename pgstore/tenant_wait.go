package pgstore

import (
	"context"
	"log/slog"
	"time"

	"github.com/oleg-tkachuk/limes/meter"
)

// defaultTenantWaitWarnAfter is how long a call may wait for its tenant's
// aggregate before the wait is logged at WARN. A charge holds the row for
// two round trips (docs/adr/0018-fewer-round-trips-per-charge.md), so a wait
// this long is a queue of calls on one tenant, not one call ahead.
const defaultTenantWaitWarnAfter = 100 * time.Millisecond

// logKeyWait is how long a call waited, as a log line says.
const logKeyWait = "wait"

// reportTenantWait reports how long l's call waited for its tenant's
// aggregate: to Config.Events, every time, and to the log at WARN past
// tenantWaitWarnAfter (docs/adr/0022-unlimited-tenant-aggregate.md,
// Decision 6).
func (s *UsageStore) reportTenantWait(ctx context.Context, l lockRequest, wait time.Duration) {
	if wait > s.tenantWaitWarnAfter {
		s.log.WarnContext(ctx, "pgstore: a call waited long for its tenant's aggregate",
			slog.String(logKeyOp, string(l.op)), slog.String(logKeyTenant, l.tenantID.String()), slog.Duration(logKeyWait, wait))
	}
	if s.events.OnTenantWait != nil {
		s.events.OnTenantWait(ctx, meter.TenantWaitEvent{TenantID: l.tenantID, Operation: l.op, Wait: wait})
	}
}
