package pgstore

import (
	"context"
	"slices"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// sqlTenantBaseTables lists the base tables of schema $1 with a tenant_id
// column: the views over them have one too, and hold no rows of their own.
const sqlTenantBaseTables = `
SELECT t.table_name FROM information_schema.tables t
JOIN information_schema.columns c USING (table_schema, table_name)
WHERE t.table_schema = $1 AND t.table_type = 'BASE TABLE' AND c.column_name = 'tenant_id'
ORDER BY t.table_name`

// TenantTables lists every base table of the schema with a tenant_id
// column, and nothing else: a migration that adds one fails here until the
// list names it.
func TestTenantTablesAreTheSchemas(t *testing.T) {
	rows, err := owner.Query(t.Context(), sqlTenantBaseTables, Schema)
	if err != nil {
		t.Fatal(err)
	}
	want, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	got := tenantTableNames()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("TenantTables = %v, the schema's tenant tables are %v", got, want)
	}
}

// Every table TenantTables lists names a disposition, and the zero value
// names none.
func TestTenantTablesNameTheirErase(t *testing.T) {
	dispositions := []TableErase{EraseRemoves, EraseKeepsUntilPurge, EraseMarks}
	if slices.Contains(dispositions, TableErase(0)) {
		t.Errorf("the zero TableErase names a disposition")
	}
	for _, table := range TenantTables() {
		if !slices.Contains(dispositions, table.Erase) {
			t.Errorf("%s: Erase = %d, names no disposition", table.Name, table.Erase)
		}
	}
}

// countTenantRows counts tenant's rows in each table TenantTables lists, in
// tx.
func countTenantRows(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (map[string]int64, error) {
	counts := map[string]int64{}
	for _, table := range TenantTables() {
		var n int64
		sql := `SELECT count(*) FROM ` + pgx.Identifier{Schema, table.Name}.Sanitize() + ` WHERE tenant_id = $1`
		if err := tx.QueryRow(ctx, sql, tenant).Scan(&n); err != nil {
			return nil, err
		}
		counts[table.Name] = n
	}
	return counts, nil
}

// eraseAndCount erases f's tenant and counts its rows left in each table,
// in the erase's transaction.
func eraseAndCount(t *testing.T, f tenantFixture) map[string]int64 {
	t.Helper()
	var counts map[string]int64
	req := limes.EraseTenantRequest{TenantID: f.mine, Note: "tenant removed", Actor: "test"}
	_, err := f.records.EraseTenant(WithTenant(t.Context(), f.mine), req, func(ctx context.Context, e Erased) error {
		var err error
		counts, err = countTenantRows(ctx, e.Tx, f.mine)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// EraseTenant does to each table what TenantTables says: no row of the
// tenant is left where it removes them and one where it marks the tenant;
// where it keeps them, rows of the records not yet due stay until Purge
// takes the records.
func TestEraseTenantDoesWhatTenantTablesSay(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.records.RevokeBiscuit(ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(t, f.child.ID, []byte("erased-copy"))}); err != nil {
		t.Fatal(err)
	}
	key := limes.ExchangeKey{TenantID: f.mine, Issuer: "https://idp.example", TokenID: "erased-exchange"}
	if _, _, err := f.records.ClaimExchange(ctx, key, f.child.ID); err != nil {
		t.Fatal(err)
	}
	erased := eraseAndCount(t, f)
	for _, table := range TenantTables() {
		n := erased[table.Name]
		switch table.Erase {
		case EraseRemoves:
			if n != 0 {
				t.Errorf("%s: %d rows of the tenant after the erase, want none", table.Name, n)
			}
		case EraseMarks:
			if n != 1 {
				t.Errorf("%s: %d rows of the tenant after the erase, want its mark", table.Name, n)
			}
		case EraseKeepsUntilPurge:
			if n == 0 {
				t.Errorf("%s: no row of the tenant's kept records after the erase", table.Name)
			}
		}
	}
	all := WithAllTenants(t.Context())
	f.clock.Advance(rlsTTL + 2*limes.MinPurgeRetention)
	if _, err := f.records.Purge(all, limes.PurgeRequest{}); err != nil {
		t.Fatal(err)
	}
	purged := eraseAndCount(t, f)
	for _, table := range TenantTables() {
		if n := purged[table.Name]; table.Erase == EraseKeepsUntilPurge && n != 0 {
			t.Errorf("%s: %d rows of the tenant after Purge, want none", table.Name, n)
		}
	}
}
