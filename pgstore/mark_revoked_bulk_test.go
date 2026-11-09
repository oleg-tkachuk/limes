package pgstore

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// bulkRoots is how many records a bulk revocation revokes at once, each
// with one child below it.
const bulkRoots = 1000

// bulkReadsPerRecord bounds how many rows of the records a bulk revocation
// reads per record it marks: a walk a level at a time reads each record a
// few times, however the planner reads them; one walk per entry reads every
// record unrevoked once per entry.
const bulkReadsPerRecord = 10

// sqlStaleRevokedStatistics leaves the records' statistics saying every
// record is revoked: a few records, each revoked, analyzed. TRUNCATE, which
// fresh runs next, keeps a column's statistics.
const sqlStaleRevokedStatistics = `
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
SELECT uuidv7(), NULL, gen_random_uuid(), 'agent', 'subject:' || i, now() + interval '1 hour', 0, '{}', '{}'
FROM generate_series(1, 100) i;

INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, now(), 'stale', 'test', false FROM limes.capabilities;

ANALYZE limes.capabilities`

// sqlSeedBulkTree records $1 roots of a tenant of their own each, and one
// child below each root.
const sqlSeedBulkTree = `
WITH roots AS (
    INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
    SELECT uuidv7(), NULL, gen_random_uuid(), 'agent', 'root:' || i, now() + interval '1 hour', 0, '{}', '{}'
    FROM generate_series(1, $1::int) i
    RETURNING id, tenant_id
)
INSERT INTO limes.capabilities (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
SELECT uuidv7(), id, tenant_id, 'agent', 'child', now() + interval '1 hour', 1, '{}', '{}' FROM roots`

// sqlRevokeRoots revokes every root in one statement.
const sqlRevokeRoots = `
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
SELECT id, tenant_id, now(), 'bulk', 'test', false FROM limes.capabilities WHERE parent_id IS NULL`

// sqlRecordsRead is how many rows of the records the transaction has read,
// by sequential and by index scans.
const sqlRecordsRead = `
SELECT coalesce(seq_tup_read, 0) + coalesce(idx_tup_fetch, 0)
FROM pg_stat_xact_user_tables WHERE schemaname = 'limes' AND relname = 'capabilities'`

// A revocation of many records in one statement marks each and every
// record below it, reading the records a bounded number of times per
// record, even where the statistics say every record is revoked already
// and so plan the walk's read of the children as a scan of every record
// unrevoked.
func TestBulkRevocationMarksInLinearReads(t *testing.T) {
	fresh(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, sqlStaleRevokedStatistics, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	fresh(t)
	if _, err := owner.Exec(ctx, sqlSeedBulkTree, bulkRoots); err != nil {
		t.Fatal(err)
	}
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before, after int64
	if err := tx.QueryRow(ctx, sqlRecordsRead).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sqlRevokeRoots); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, sqlRecordsRead).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var unmarked int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM limes.capabilities WHERE NOT revoked`).Scan(&unmarked); err != nil {
		t.Fatal(err)
	}
	if unmarked != 0 {
		t.Errorf("%d records left unmarked by the bulk revocation, want none", unmarked)
	}
	records := int64(2 * bulkRoots)
	if read := after - before; read > bulkReadsPerRecord*records {
		t.Errorf("the bulk revocation of %d records read %d rows of the records, want at most %d", records, read, bulkReadsPerRecord*records)
	}
}
