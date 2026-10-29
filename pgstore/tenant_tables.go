package pgstore

// Schema is the PostgreSQL schema every table of the store lives in.
const Schema = schema

// TableErase is what Store.EraseTenant does to a tenant's rows in a table.
type TableErase int

// What Store.EraseTenant does to a tenant's rows, table by table. The zero
// value names none of them.
const (
	// EraseRemoves: the erase removes every row of the tenant.
	EraseRemoves TableErase = iota + 1
	// EraseKeepsUntilPurge: the rows of records not yet due stay, revoked,
	// until Store.Purge removes the records and these rows with them.
	EraseKeepsUntilPurge
	// EraseMarks: the erase adds the tenant's row, which UsageStore.PurgeOrphans
	// removes once none of the tenant's records is left.
	EraseMarks
)

// TenantTable is a base table of Schema that holds tenants' rows, under a
// tenant_id column its row-level security policy keys on.
type TenantTable struct {
	// Name is the table's name in Schema.
	Name string
	// Erase is what Store.EraseTenant does to the tenant's rows in it.
	Erase TableErase
}

// TenantTables returns every table of Schema that holds tenants' rows, with
// what Store.EraseTenant does to them: the list a tool that checks a
// tenant's erase, or exports its rows, reads instead of the catalog, where
// the views over these tables carry a tenant_id column too. The slice is the
// caller's.
func TenantTables() []TenantTable {
	return []TenantTable{
		{Name: "capabilities", Erase: EraseKeepsUntilPurge},
		{Name: "revocations", Erase: EraseKeepsUntilPurge},
		{Name: "biscuit_revocations", Erase: EraseKeepsUntilPurge},
		{Name: "exchanges", Erase: EraseKeepsUntilPurge},
		{Name: "reservations", Erase: EraseRemoves},
		{Name: "tenant_budgets", Erase: EraseRemoves},
		{Name: "usage", Erase: EraseRemoves},
		{Name: "copy_usage", Erase: EraseRemoves},
		{Name: "charges", Erase: EraseRemoves},
		{Name: "erased_tenants", Erase: EraseMarks},
	}
}
