package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// The settings the row-level security policies read (migrations/
// 003_row_security.sql), each set local to one transaction.
const (
	// settingTenant holds the tenant a transaction is scoped to.
	settingTenant = "limes.tenant_id"
	// settingAllTenants is settingOn in a transaction scoped to every
	// tenant, and settingRead in one that may read every tenant's rows and
	// write none.
	settingAllTenants = "limes.all_tenants"
	settingOn         = "on"
	settingRead       = "read"
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
	// readOnly narrows all to reading.
	readOnly bool
}

// WithTenant scopes the calls made with ctx to one tenant: under the row-level
// security of the store's schema, a role that does not own the tables sees
// and writes that tenant's rows only.
func WithTenant(ctx context.Context, tenant uuid.UUID) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{tenant: tenant})
}

// WithAllTenants scopes the calls made with ctx to every tenant, to read
// and write: for housekeeping (Store.Purge, UsageStore.ReleaseExpired and
// PurgeOrphans). A read across tenants takes WithAllTenantsReadOnly. A call
// scoped to none sees no row at all.
func WithAllTenants(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{all: true})
}

// WithAllTenantsReadOnly scopes the calls made with ctx to reading every
// tenant's rows — ListTenantBudgets, a dashboard, an admin lookup — and
// writing none: the schema's policies admit no write under it, and a call
// that writes is refused with limes.ErrScopeMismatch before it begins. A
// Scope of the consumer's gets the same refusal by setting limes.all_tenants
// to 'read' (Config.Scope).
func WithAllTenantsReadOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{all: true, readOnly: true})
}

// errReadOnlyScope is a call that writes, made under a read-only scope.
var errReadOnlyScope = fmt.Errorf("%w: pgstore: a read-only scope admits no write", limes.ErrScopeMismatch)

// readOnly reports whether ctx is scoped by WithAllTenantsReadOnly.
func readOnly(ctx context.Context) bool {
	s, ok := ctx.Value(scopeKey{}).(scope)
	return ok && s.readOnly
}

// sqlScopeReadOnly is whether the transaction is scoped to reading only, as
// the schema's settings hold it: unset reads as not.
const sqlScopeReadOnly = `SELECT coalesce(current_setting('` + settingAllTenants + `', true) = '` + settingRead + `', false)`

// checkSchemaReadOnly refuses with limes.ErrScopeMismatch a transaction that
// a Scope of the consumer's scoped to reading only, before the call writes:
// the schema's policies would let the writes through as no rows, or fail
// them with the database's own error. It is the read-only check of every
// call that writes under a Scope the store did not set itself.
func checkSchemaReadOnly(ctx context.Context, tx pgx.Tx) error {
	var ro bool
	if err := tx.QueryRow(ctx, sqlScopeReadOnly).Scan(&ro); err != nil {
		return err
	}
	if ro {
		return errReadOnlyScope
	}
	return nil
}

// ScopeFromContext is the Scope a store uses when Config sets none: it sets
// the settings the schema's policies read from WithTenant, WithAllTenants or
// WithAllTenantsReadOnly.
func ScopeFromContext(ctx context.Context, tx pgx.Tx) error {
	s, ok := ctx.Value(scopeKey{}).(scope)
	switch {
	case !ok:
		return nil
	case s.readOnly:
		_, err := tx.Exec(ctx, sqlSetLocal, settingAllTenants, settingRead)
		return err
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

// sqlScopeAdmits is whether the transaction may see tenant's rows: the role
// is not bound by row-level security on the schema's tables — the owner, or
// one that bypasses it — or the schema's settings admit tenant, as its
// policies read them. tenant_visible is NULL in a transaction scoped to no
// tenant, which a policy reads as false and so does this.
const sqlScopeAdmits = `SELECT NOT row_security_active('` + schema + `.capabilities') OR coalesce(` + schema + `.tenant_visible($1), false)`

// CheckSchemaScope refuses with limes.ErrScopeMismatch a transaction whose
// scope, as the schema's settings hold it, does not admit tenant — scoped to
// another tenant, or to none — when row-level security binds its role. It is
// a Config.CheckScope for a Scope of the consumer's that sets those settings.
func CheckSchemaScope(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	var admits bool
	if err := tx.QueryRow(ctx, sqlScopeAdmits, tenant).Scan(&admits); err != nil {
		return err
	}
	if !admits {
		return scopeMismatch(tenant)
	}
	return nil
}

// checkContextScope is the CheckScope of the default Scope: a call's
// WithTenant or WithAllTenants decides without a round trip, and one with
// neither is checked against the database, where an owner is not bound.
func checkContextScope(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	s, ok := ctx.Value(scopeKey{}).(scope)
	switch {
	case !ok:
		return CheckSchemaScope(ctx, tx, tenant)
	case s.all, s.tenant == tenant:
		return nil
	}
	return scopeMismatch(tenant)
}

// scopeMismatch is limes.ErrScopeMismatch for a call naming tenant.
func scopeMismatch(tenant uuid.UUID) error {
	return fmt.Errorf("%w: pgstore: tenant %s", limes.ErrScopeMismatch, tenant)
}
