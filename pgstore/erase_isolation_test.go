package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes"
)

const (
	// isolationSetting is the session setting a database's default
	// isolation level comes from.
	isolationSetting        = "default_transaction_isolation"
	isolationRepeatableRead = "repeatable read"
	// sqlWaitingOnAdvisory counts the sessions waiting on an advisory lock.
	sqlWaitingOnAdvisory = `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND wait_event = 'advisory'`
	// waitPoll is how often the test looks for the Insert to be waiting,
	// and waitDeadline how long it looks.
	waitPoll     = 10 * time.Millisecond
	waitDeadline = 10 * time.Second
)

// An Insert that waits on its tenant's erase lock sees the erase it waited
// for, whatever isolation level the database defaults to: the store opens
// READ COMMITTED transactions, whose statements read what committed before
// them, not what had when the transaction began.
func TestInsertSeesAnEraseItWaitedForUnderAnyDefaultIsolation(t *testing.T) {
	fresh(t)
	ctx := t.Context()
	cfg := app.Config()
	cfg.ConnConfig.RuntimeParams[isolationSetting] = isolationRepeatableRead
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	clock := newFakeClock()
	records, err := NewStore(Config{DB: pool, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()

	erase, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = erase.Rollback(context.WithoutCancel(ctx)) }()
	if err := markErased(ctx, erase, tenant, clock.Now()); err != nil {
		t.Fatal(err)
	}

	inserted := make(chan error, 1)
	go func() {
		c := limes.Capability{
			ID: uuid.New(), ExpiresAt: clock.Now().Add(rlsTTL),
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: testUnit}},
		}
		inserted <- records.Insert(WithTenant(ctx, tenant), limes.InsertRequest{Capability: c})
	}()
	for deadline := time.Now().Add(waitDeadline); ; time.Sleep(waitPoll) {
		var waiting int
		if err := owner.QueryRow(ctx, sqlWaitingOnAdvisory).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Insert never waited on the erase lock")
		}
	}
	if err := erase.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-inserted; !errors.Is(err, limes.ErrTenantErased) {
		t.Errorf("Insert that waited for an erase, under a REPEATABLE READ default: err = %v, want ErrTenantErased", err)
	}
}
