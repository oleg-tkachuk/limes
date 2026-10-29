// pgstore is the reference store of limes over PostgreSQL. It is a module of
// its own so that the limes module keeps its graph free of a database driver
// (limes/isolation_test.go).
module github.com/oleg-tkachuk/limes/pgstore

go 1.27.0

toolchain go1.27.3

// The replace builds this module against the limes of the same commit, which
// is what CI tests; a consumer of this module, whose build ignores the
// replace, gets the version required here.
replace github.com/oleg-tkachuk/limes => ../

// v0.1.0 passes a tenant budget filter to SQL as its number, so with limes
// v0.57.0 or later, which renumbered the filters, ListTenantBudgets lists the
// wrong tenants without an error.
retract v0.1.0

require (
	github.com/jackc/pgx/v5 v5.11.0
	github.com/jackc/tern/v2 v2.4.3
	github.com/oleg-tkachuk/limes v0.138.0
)

require (
	dario.cat/mergo v1.0.2 // indirect
	github.com/Masterminds/goutils v1.1.1 // indirect
	github.com/Masterminds/semver/v3 v3.5.0 // indirect
	github.com/Masterminds/sprig/v3 v3.3.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/huandu/xstrings v1.5.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
