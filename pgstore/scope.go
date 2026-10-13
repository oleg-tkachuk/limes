package pgstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The settings the row-level security policies read (migrations/
// 003_row_security.sql), each set local to one transaction.
const (
	// settingTenant holds the tenant a transaction is scoped to.
	settingTenant = "limes.tenant_id"
	// settingAllTenants is settingOn in a transaction scoped to every tenant.
	settingAllTenants = "limes.all_tenants"
	settingOn         = "on"
)

// sqlSetLocal sets a setting for the rest of the transaction only, so a
// pooled connection carries nothing into the next one.
const sqlSetLocal = `SELECT set_config($1, $2, true)`

// scopeKey is the context key of a call's scope.
type scopeKey struct{}

// scope is what a call's context says it may see.
type scope struct {
	tenant uuid.UUID
	all    bool
}

// WithTenant scopes the calls made with ctx to one tenant: under the row-level
// security of the store's schema, a role that does not own the tables sees
// and writes that tenant's rows only.
func WithTenant(ctx context.Context, tenant uuid.UUID) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{tenant: tenant})
}

// WithAllTenants scopes the calls made with ctx to every tenant: for
// housekeeping (Store.Purge, UsageStore.ReleaseExpired and PurgeOrphans) and
// for reads across tenants (ListTenantBudgets). A call scoped to neither sees
// no row at all.
func WithAllTenants(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{all: true})
}

// ScopeFromContext is the Scope a store uses when Config sets none: it sets
// the settings the schema's policies read from WithTenant or WithAllTenants.
func ScopeFromContext(ctx context.Context, tx pgx.Tx) error {
	s, ok := ctx.Value(scopeKey{}).(scope)
	switch {
	case !ok:
		return nil
	case s.all:
		return allTenants(ctx, tx)
	}
	_, err := tx.Exec(ctx, sqlSetLocal, settingTenant, s.tenant.String())
	return err
}

// allTenants scopes tx to every tenant.
func allTenants(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, sqlSetLocal, settingAllTenants, settingOn)
	return err
}
