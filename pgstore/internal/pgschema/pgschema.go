// Package pgschema holds what pgstore and pgstore/migrate must spell alike:
// the schema's name, its version table, the version the migrations reach,
// and the setting a migration is scoped by. It imports nothing, so either
// can import it without importing the other.
package pgschema

// Name is the PostgreSQL schema every table of the store lives in. The
// migrations and every query spell it so; a mismatch fails each of them.
const Name = "limes"

// VersionTableName is the table that records which migrations have run, and
// VersionTable its name in the schema.
const (
	VersionTableName = "schema_version"
	VersionTable     = Name + "." + VersionTableName
)

// Version is the last migration pgstore/migrate carries, and so the version
// the stores query: tern numbers the migrations from one without a gap.
// pgstore/migrate's tests hold it to the migrations it embeds.
const Version = 30

// SettingAllTenants is the setting the row-level security policies read as
// "every tenant is in scope" when it holds SettingAllTenantsOn.
const (
	SettingAllTenants   = "limes.all_tenants"
	SettingAllTenantsOn = "on"
)
