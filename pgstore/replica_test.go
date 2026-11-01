package pgstore

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

const (
	// readOnlySetting is the session setting that makes every transaction
	// read-only by default, as every transaction on a hot standby is.
	readOnlySetting = "default_transaction_read_only"
	settingOn       = "on"
	// sqlStateReadOnlyTx is PostgreSQL's read_only_sql_transaction.
	sqlStateReadOnlyTx = "25006"
)

// replicaPool is a pool of the application's role whose every transaction
// is read-only: a hot standby's, as far as what the store may send it.
func replicaPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg := app.Config()
	cfg.ConnConfig.RuntimeParams[readOnlySetting] = settingOn
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The reports — the census, the record listings and the charge sums, of
// one tenant and of every tenant — only read, so they run on a read
// replica; the ledger's housekeeping writes, and is refused there.
func TestReportsRunOnAReadReplica(t *testing.T) {
	f := newTenantFixture(t)
	at := f.clock.Now()
	if _, err := owner.Exec(t.Context(), sqlLedgerCharge, uuid.New(), f.parent.ID, f.mine, int64(limes.NanosPerUnit), testUnit, at); err != nil {
		t.Fatal(err)
	}
	replica := replicaPool(t)
	cfg := Config{DB: replica, Now: f.clock.Now}
	census, err := NewCensus(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLedger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mine, every := WithTenant(t.Context(), f.mine), WithAllTenantsReadOnly(t.Context())
	window := meter.SumChargesRequest{From: at.Add(-time.Hour), To: at.Add(time.Hour)}
	mineSum, everySum := window, window
	mineSum.TenantID, everySum.AllTenants = f.mine, true

	for name, report := range map[string]func() (int, error){
		"CountRecords": func() (int, error) {
			p, err := census.CountRecords(mine, limes.CountRecordsRequest{TenantID: f.mine})
			return len(p.Items), err
		},
		"CountRecords of every tenant": func() (int, error) {
			p, err := census.CountRecords(every, limes.CountRecordsRequest{AllTenants: true})
			return len(p.Items), err
		},
		"ListRecords": func() (int, error) {
			p, err := census.ListRecords(mine, limes.ListRecordsRequest{TenantID: f.mine})
			return len(p.Items), err
		},
		"ListRecords of every tenant": func() (int, error) {
			p, err := census.ListRecords(every, limes.ListRecordsRequest{AllTenants: true})
			return len(p.Items), err
		},
		"GetRecordListing": func() (int, error) {
			_, err := census.GetRecordListing(mine, f.child.ID)
			return 1, err
		},
		"SumCharges": func() (int, error) {
			p, err := ledger.SumCharges(mine, mineSum)
			return len(p.Items), err
		},
		"SumCharges of every tenant": func() (int, error) {
			p, err := ledger.SumCharges(every, everySum)
			return len(p.Items), err
		},
	} {
		if n, err := report(); err != nil || n == 0 {
			t.Errorf("%s on a replica = %d items, %v; want the report", name, n, err)
		}
	}

	_, err = ledger.PurgeCharges(WithAllTenants(t.Context()), meter.PurgeChargesRequest{})
	if pgErr := (*pgconn.PgError)(nil); !errors.As(err, &pgErr) || pgErr.Code != sqlStateReadOnlyTx {
		t.Errorf("PurgeCharges on a replica = %v, want the read-only refusal", err)
	}
}
