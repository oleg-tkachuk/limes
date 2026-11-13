package pgstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

const (
	// sqlRequestsOf reads the request count of one capability's counters.
	sqlRequestsOf = `SELECT request_count FROM ` + schema + `.usage WHERE capability_id = $1`
	// sqlBumpCount counts the bumps rows of one capability and reference.
	sqlBumpCount = `SELECT count(*) FROM ` + schema + `.bumps WHERE capability_id = $1 AND external_ref = $2`
	// sqlSeedHiddenBump records a bump of $1 under $2 for tenant $3, as the
	// owner, whom row-level security does not bind.
	sqlSeedHiddenBump = `
INSERT INTO ` + schema + `.bumps (capability_id, external_ref, tenant_id, copies, request_count, bumped_at)
VALUES ($1, $2, $3, '{}', 1, now())`
)

// bumpTxRef is the reference the bump tests count under, bumpTxLeased the
// requests the leased one leases, and bumpTxAged how far past the ledger's
// retention a purged bump is.
const (
	bumpTxRef    = "request-1"
	bumpTxLeased = 5
	bumpTxAged   = time.Second
)

// bumpPaths are the two ways a Bump counts: through the whole chain, and
// under a lease of requests of its capability, which it opens.
var bumpPaths = []struct {
	name  string
	lease bool
}{{"unleased", false}, {"leased", true}}

// leaseChild opens a lease of requests of the fixture's child when lease.
func leaseChild(t *testing.T, ctx context.Context, f tenantFixture, lease bool) {
	t.Helper()
	if !lease {
		return
	}
	if _, err := f.usage.Lease(ctx, meter.LeaseRequest{Subject: meterSubject(f.child.ID, f.mine), Requests: bumpTxLeased}); err != nil {
		t.Fatal(err)
	}
}

// onBumped runs once the bump's writes are sent, before COMMIT, on the
// bump's own transaction, under a lease or not: through TxFrom it reads the
// capability's count with this request in it and, for a keyed bump, its
// bumps row.
func TestOnBumpedRunsAfterTheWrites(t *testing.T) {
	for _, path := range bumpPaths {
		for _, tc := range []struct {
			name string
			ref  string
			rows int
		}{
			{name: "unkeyed", ref: "", rows: 0},
			{name: "keyed", ref: bumpTxRef, rows: 1},
		} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				f := newTenantFixture(t)
				ctx := WithTenant(t.Context(), f.mine)
				leaseChild(t, ctx, f, path.lease)
				var requests int64
				var rows int
				ran := false
				onBumped := func(ctx context.Context, b meter.Bumped) error {
					ran = true
					tx, ok := TxFrom(ctx)
					if !ok {
						t.Fatal("onBumped's context holds no transaction")
					}
					if err := tx.QueryRow(ctx, sqlRequestsOf, f.child.ID).Scan(&requests); err != nil {
						return err
					}
					return tx.QueryRow(ctx, sqlBumpCount, f.child.ID, tc.ref).Scan(&rows)
				}
				got, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine), ExternalRef: tc.ref}, onBumped)
				if err != nil || !ran || got.RequestCount != 1 {
					t.Fatalf("bump = %+v, %v, onBumped ran %t; want the first request", got, err, ran)
				}
				if requests != got.RequestCount || rows != tc.rows {
					t.Errorf("inside onBumped: %d requests and %d bumps rows, want %d and %d", requests, rows, got.RequestCount, tc.rows)
				}
			})
		}
	}
}

// onBumped may not end the bump's transaction: Commit and Rollback through
// TxFrom are refused, and the bump commits.
func TestOnBumpedKeepsTheTransactionTheStores(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	onBumped := func(ctx context.Context, _ meter.Bumped) error {
		tx, _ := TxFrom(ctx)
		if err := tx.Commit(ctx); !errors.Is(err, ErrChargeTransaction) {
			t.Errorf("Commit in onBumped: err = %v, want ErrChargeTransaction", err)
		}
		if err := tx.Rollback(ctx); !errors.Is(err, ErrChargeTransaction) {
			t.Errorf("Rollback in onBumped: err = %v, want ErrChargeTransaction", err)
		}
		return nil
	}
	if _, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine)}, onBumped); err != nil {
		t.Fatalf("a bump whose onBumped tried to end its transaction: %v", err)
	}
	if u, err := f.usage.GetUsage(ctx, f.child.ID); err != nil || u.RequestCount != 1 {
		t.Errorf("the child after the bump = %+v, %v; want 1 request", u, err)
	}
}

// SQL that ends the bump's transaction refuses the bump as it refuses a
// charge, under a lease or not: its writes went before onBumped, so a
// COMMIT of onBumped's committed them and a ROLLBACK undid them, and the
// call says it does not know which.
func TestOnBumpedRefusesATransactionEndedBySQL(t *testing.T) {
	for _, path := range bumpPaths {
		for _, tc := range []struct {
			sql      string
			requests int64
		}{
			{"COMMIT", 1},
			{"ROLLBACK", 0},
			{"COMMIT; BEGIN", 1},
			{"ROLLBACK; BEGIN", 0},
		} {
			t.Run(path.name+"/"+tc.sql, func(t *testing.T) {
				f := newTenantFixture(t)
				ctx := WithTenant(t.Context(), f.mine)
				leaseChild(t, ctx, f, path.lease)
				onBumped := func(ctx context.Context, _ meter.Bumped) error {
					tx, _ := TxFrom(ctx)
					_, _ = tx.Exec(ctx, tc.sql) // what it ended is the store's to find
					return nil
				}
				_, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine)}, onBumped)
				if !errors.Is(err, ErrChargeTransaction) || !errors.Is(err, meter.ErrOutcomeUnknown) {
					t.Errorf("a bump whose onBumped ran %s: err = %v, want ErrChargeTransaction, outcome unknown", tc.sql, err)
				}
				if u, err := f.usage.GetUsage(ctx, f.child.ID); err == nil && u.RequestCount != tc.requests {
					t.Errorf("the child after %s = %+v; want %d requests", tc.sql, u, tc.requests)
				}
			})
		}
	}
}

// A callback that sets the session's role is refused, the bump rolled back
// with it, as a charge's is.
func TestOnBumpedRefusesACallbacksRole(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	onBumped := func(ctx context.Context, _ meter.Bumped) error {
		tx, _ := TxFrom(ctx)
		_, err := tx.Exec(ctx, sqlSetRole)
		return err
	}
	_, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine), ExternalRef: bumpTxRef}, onBumped)
	if !errors.Is(err, ErrChargeTransaction) || errors.Is(err, meter.ErrOutcomeUnknown) {
		t.Errorf("a bump whose onBumped set the role: err = %v, want ErrChargeTransaction, the outcome known", err)
	}
	if u, err := f.usage.GetUsage(ctx, f.child.ID); err == nil && u.RequestCount != 0 {
		t.Errorf("a refused bump counted %d requests", u.RequestCount)
	}
	checkPoolSetsNoRole(t, "the refused bump")
}

// A reference another tenant's bump of the capability holds is hidden from
// the call by row-level security, and still names that bump: the call is
// ErrExternalRefConflict, never a constraint violation, and counts nothing.
func TestAnotherTenantsBumpRefConflicts(t *testing.T) {
	for _, path := range bumpPaths {
		t.Run(path.name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			leaseChild(t, ctx, f, path.lease)
			if _, err := owner.Exec(t.Context(), sqlSeedHiddenBump, f.child.ID, bumpTxRef, f.theirs); err != nil {
				t.Fatal(err)
			}
			r, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine), ExternalRef: bumpTxRef}, nil)
			if !errors.Is(err, meter.ErrExternalRefConflict) || !errors.Is(err, limes.ErrConflict) {
				t.Errorf("Bump under another tenant's reference = %+v, %v; want ErrExternalRefConflict", r, err)
			}
			if u, err := f.usage.GetUsage(ctx, f.child.ID); err == nil && u.RequestCount != 0 {
				t.Errorf("a conflicting Bump counted %d requests", u.RequestCount)
			}
		})
	}
}

// sqlCreateLookalikeBumpKey is a table of the consumer's with a unique
// constraint named as the store's key of a keyed bump.
const sqlCreateLookalikeBumpKey = `CREATE TEMP TABLE consumer_rows (n int CONSTRAINT bumps_pkey UNIQUE) ON COMMIT DROP`

// A unique violation of the consumer's own table that an onBumped hits is
// the consumer's error, as it is, even under the name of the store's key of
// a keyed bump.
func TestBumpKeepsACallbacksOwnUniqueViolation(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	onBumped := func(ctx context.Context, _ meter.Bumped) error {
		tx, _ := TxFrom(ctx)
		if _, err := tx.Exec(ctx, sqlCreateLookalikeBumpKey); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sqlViolateLookalikeKey)
		return err
	}
	_, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine), ExternalRef: bumpTxRef}, onBumped)
	var pgErr *pgconn.PgError
	if errors.Is(err, meter.ErrExternalRefConflict) || !errors.As(err, &pgErr) || pgErr.Code != uniqueViolation {
		t.Errorf("a Bump whose onBumped violated its own key: %v, want that violation, no ErrExternalRefConflict", err)
	}
}

// A keyed bump goes with its capability's counters (DeleteUsage), its
// tenant's usage (EraseTenantUsage, EraseTenant) and the ledger's retention
// (PurgeCharges), and stays through PurgeOrphans: what the rows of
// limes.bumps hold after each.
func TestKeyedBumpRowsGoWithTheirCapabilityAndTenant(t *testing.T) {
	bumped := func(t *testing.T, ctx context.Context, f tenantFixture) {
		t.Helper()
		if _, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine), ExternalRef: bumpTxRef}, nil); err != nil {
			t.Fatal(err)
		}
	}
	rows := func(t *testing.T, f tenantFixture) int {
		t.Helper()
		var n int
		if err := owner.QueryRow(t.Context(), sqlBumpCount, f.child.ID, bumpTxRef).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, remove := range map[string]func(t *testing.T, ctx context.Context, f tenantFixture) error{
		"DeleteUsage": func(_ *testing.T, ctx context.Context, f tenantFixture) error {
			return f.usage.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: f.child.ID})
		},
		"EraseTenantUsage": func(_ *testing.T, ctx context.Context, f tenantFixture) error {
			_, err := f.usage.EraseTenantUsage(ctx, meter.EraseTenantUsageRequest{TenantID: f.mine})
			return err
		},
		"EraseTenant": func(_ *testing.T, ctx context.Context, f tenantFixture) error {
			_, err := f.records.EraseTenant(ctx, limes.EraseTenantRequest{TenantID: f.mine}, nil)
			return err
		},
		"PurgeCharges": func(t *testing.T, ctx context.Context, f tenantFixture) error {
			ledger, err := NewLedger(Config{DB: app, Now: f.clock.Now})
			if err != nil {
				t.Fatal(err)
			}
			f.clock.Advance(meter.MinChargeRetention + bumpTxAged)
			res, err := ledger.PurgeCharges(ctx, meter.PurgeChargesRequest{})
			if err == nil && res.Purged != 1 {
				t.Errorf("PurgeCharges = %+v, want the keyed bump purged", res)
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			bumped(t, ctx, f)
			if err := remove(t, ctx, f); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if n := rows(t, f); n != 0 {
				t.Errorf("bumps rows of the child after %s = %d, want none", name, n)
			}
		})
	}
	t.Run("PurgeOrphans", func(t *testing.T) {
		f := newTenantFixture(t)
		ctx := WithTenant(t.Context(), f.mine)
		bumped(t, ctx, f)
		if _, err := owner.Exec(t.Context(), deleteRecord, f.child.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.usage.PurgeOrphans(ctx, meter.PurgeOrphansRequest{}); err != nil {
			t.Fatal(err)
		}
		if n := rows(t, f); n != 1 {
			t.Errorf("bumps rows of the child after PurgeOrphans = %d, want its one, to replay", n)
		}
		r, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: meterSubject(f.child.ID, f.mine), ExternalRef: bumpTxRef}, nil)
		if err != nil || !r.Replayed || r.RequestCount != 1 {
			t.Errorf("the keyed bump again once its capability is gone = %+v, %v; want the first, replayed", r, err)
		}
	})
}

const (
	// sqlWaitingOnRows counts the sessions waiting on a row's lock.
	sqlWaitingOnRows = `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND wait_event <> 'advisory'`
	// raceDeliveries is how many deliveries of one reference wait for the
	// counter together: the pool's every connection.
	raceDeliveries = appPoolConns
)

// Deliveries of one reference that both read it missing, then wait for the
// capability's counter, count it once: the one that locks it second reads
// the reference again under the lock and replays the first, under a lease
// or not, rather than counting twice or failing on its key.
func TestKeyedBumpsWaitingTogetherCountOnce(t *testing.T) {
	for _, path := range bumpPaths {
		t.Run(path.name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			leaseChild(t, ctx, f, path.lease)
			subject := meterSubject(f.child.ID, f.mine)
			// A committed counter row for the holder to lock.
			if _, err := f.usage.Bump(ctx, meter.BumpRequest{Subject: subject}, nil); err != nil {
				t.Fatal(err)
			}
			holder, err := owner.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
			if _, err := holder.Exec(ctx, sqlLockCounter, f.child.ID); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			receipts, errs := make([]meter.BumpReceipt, raceDeliveries), make([]error, raceDeliveries)
			for i := range raceDeliveries {
				wg.Go(func() {
					receipts[i], errs[i] = f.usage.Bump(ctx, meter.BumpRequest{Subject: subject, ExternalRef: bumpTxRef}, nil)
				})
			}
			for deadline := time.Now().Add(waitDeadline); ; time.Sleep(waitPoll) {
				var waiting int
				if err := owner.QueryRow(ctx, sqlWaitingOnRows).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting >= raceDeliveries {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%d of %d deliveries waited for the counter", waiting, raceDeliveries)
				}
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			wg.Wait()
			fresh := 0
			for i, r := range receipts {
				if errs[i] != nil || r.RequestCount != 2 {
					t.Errorf("delivery %d = %+v, %v; want the count of 2", i, r, errs[i])
				}
				if !r.Replayed {
					fresh++
				}
			}
			if fresh != 1 {
				t.Errorf("%d of %d deliveries counted, want 1", fresh, raceDeliveries)
			}
			if u, err := f.usage.GetUsage(ctx, f.child.ID); err != nil || u.RequestCount != 2 {
				t.Errorf("the child after the deliveries = %+v, %v; want 2 requests", u, err)
			}
		})
	}
}
