package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// The settings the row-level security policies read (migrations/
// 003_row_security.sql), each set local to one transaction. A Scope of the
// consumer's (Config.Scope) that sets them is scoped as the package's own
// is, and CheckSchemaScope reads them back.
const (
	// SettingTenant holds the tenant a transaction is scoped to, as a
	// UUID's text.
	SettingTenant = "limes.tenant_id"
	// SettingAllTenants is SettingAllTenantsOn in a transaction scoped to
	// every tenant, and SettingAllTenantsRead in one that may read every
	// tenant's rows and write none.
	SettingAllTenants     = "limes.all_tenants"
	SettingAllTenantsOn   = "on"
	SettingAllTenantsRead = "read"
)

// sqlSetLocal sets a setting for the rest of the transaction only, so a
// pooled connection carries nothing into the next one.
const sqlSetLocal = `SELECT set_config($1, $2, true)`

// sqlSetScope sets both of the settings the policies read, for the rest of
// the transaction: the tenant, then whether every tenant is in scope.
const sqlSetScope = `SELECT set_config($1, $2, true), set_config($3, $4, true)`

// settingUnset is a setting the policies read as not set.
const settingUnset = ""

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
const sqlScopeReadOnly = `SELECT coalesce(current_setting('` + SettingAllTenants + `', true) = '` + SettingAllTenantsRead + `', false)`

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
// WithAllTenantsReadOnly. It sets both of them every time — the one the scope
// does not use to nothing — so a default the role, the database or the
// session gives either one cannot widen the scope: a call scoped to no
// tenant sees no row, whatever the defaults.
func ScopeFromContext(ctx context.Context, tx pgx.Tx) error {
	tenant, all := settingUnset, settingUnset
	if s, ok := ctx.Value(scopeKey{}).(scope); ok {
		switch {
		case s.readOnly:
			all = SettingAllTenantsRead
		case s.all:
			all = SettingAllTenantsOn
		default:
			tenant = s.tenant.String()
		}
	}
	_, err := tx.Exec(ctx, sqlSetScope, SettingTenant, tenant, SettingAllTenants, all)
	return err
}

// sqlScopeAdmits is whether the transaction may see tenant's rows: the role
// is not bound by row-level security on the schema's tables — a superuser,
// or one with BYPASSRLS — or the schema's settings admit tenant, as its
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
// neither is checked against the database, where a role the policies do not
// bind is admitted.
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

// sqlScopeAdmitsEvery is whether the transaction may see every tenant's
// rows: the role is not bound by row-level security on the schema's tables,
// or the schema's settings scope it to every tenant, to read and write or to
// read.
const sqlScopeAdmitsEvery = `SELECT NOT row_security_active('` + schema + `.capabilities') OR coalesce(current_setting('` + SettingAllTenants + `', true) IN ('` + SettingAllTenantsOn + `', '` + SettingAllTenantsRead + `'), false)`

// sqlScopeAdmitsAny is whether the transaction may see any tenant's rows:
// the role is not bound by row-level security on the schema's tables, or the
// schema's settings scope it to every tenant or to one.
const sqlScopeAdmitsAny = `SELECT NOT row_security_active('` + schema + `.capabilities') OR coalesce(current_setting('` + SettingAllTenants + `', true) IN ('` + SettingAllTenantsOn + `', '` + SettingAllTenantsRead + `'), false) OR coalesce(current_setting('` + SettingTenant + `', true), '') <> ''`

// errScopeNone is a call that acts on the rows its scope reaches, made under
// a scope that reaches none.
var errScopeNone = fmt.Errorf("%w: pgstore: the scope admits no tenant", limes.ErrScopeMismatch)

// checkSchemaAdmitsAny refuses with limes.ErrScopeMismatch a transaction
// whose scope admits no tenant at all, when row-level security binds its
// role: a call that acts on every row its scope reaches — a purge — would
// otherwise find none and report doing nothing, call after call.
func checkSchemaAdmitsAny(ctx context.Context, tx pgx.Tx) error {
	var admits bool
	if err := tx.QueryRow(ctx, sqlScopeAdmitsAny).Scan(&admits); err != nil {
		return err
	}
	if !admits {
		return errScopeNone
	}
	return nil
}

// errScopeNotEvery is a call across every tenant made under a scope that
// admits fewer.
var errScopeNotEvery = fmt.Errorf("%w: pgstore: the scope does not admit every tenant", limes.ErrScopeMismatch)

// checkSchemaAdmitsEvery refuses with limes.ErrScopeMismatch a transaction
// whose scope, as the schema's settings hold it, does not admit every
// tenant, when row-level security binds its role: a call across every
// tenant would otherwise count one tenant's rows as everyone's.
func checkSchemaAdmitsEvery(ctx context.Context, tx pgx.Tx) error {
	var admits bool
	if err := tx.QueryRow(ctx, sqlScopeAdmitsEvery).Scan(&admits); err != nil {
		return err
	}
	if !admits {
		return errScopeNotEvery
	}
	return nil
}

// checkContextAdmitsEvery is checkSchemaAdmitsEvery for the default Scope: a
// call's WithAllTenants or WithAllTenantsReadOnly admits it without a round
// trip, WithTenant refuses it, and one with neither is checked against the
// database, where a role the policies do not bind is admitted.
func checkContextAdmitsEvery(ctx context.Context, tx pgx.Tx) error {
	s, ok := ctx.Value(scopeKey{}).(scope)
	switch {
	case !ok:
		return checkSchemaAdmitsEvery(ctx, tx)
	case s.all:
		return nil
	}
	return errScopeNotEvery
}
