package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oleg-tkachuk/limes/meter"
)

// sqlSetRole sets the session's role, not the transaction's: a SET a
// callback leaves would outlast the call on the pooled connection were
// the call to commit.
const sqlSetRole = `SET ROLE ` + appUser

// sqlRole reads the session's role setting.
const sqlRole = `SELECT current_setting('role')`

// roleNone is the role setting of a session that set none.
const roleNone = "none"

// A callback that sets the session's role is refused, the call rolled back
// with it, so the role does not outlast the call on the pooled connection:
// the next call on it would run as that role.
func TestTxFromRefusesACallbacksRole(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	onCharged := func(ctx context.Context, _ meter.Charged) error {
		tx, _ := TxFrom(ctx)
		_, err := tx.Exec(ctx, sqlSetRole)
		return err
	}
	_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged)
	if !errors.Is(err, ErrChargeTransaction) || errors.Is(err, meter.ErrOutcomeUnknown) {
		t.Errorf("a charge whose onCharged set the role: err = %v, want ErrChargeTransaction, the outcome known", err)
	}
	if u, err := f.usage.GetUsage(ctx, f.child.ID); err == nil && u.SpentAmount != 0 {
		t.Errorf("a refused charge spent %v", u.SpentAmount)
	}
	// Every connection of the store's pool, held at once, has set no role.
	var conns []*pgxpool.Conn
	defer func() {
		for _, c := range conns {
			c.Release()
		}
	}()
	for range appPoolConns {
		c, err := app.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var role string
		if err := c.QueryRow(t.Context(), sqlRole).Scan(&role); err != nil {
			t.Fatal(err)
		}
		if role != roleNone {
			t.Errorf("a pooled connection's role after the refused charge = %q, want %q", role, roleNone)
		}
	}
}
