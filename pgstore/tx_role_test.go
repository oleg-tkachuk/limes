package pgstore

import (
	"context"
	"errors"
	"fmt"
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
	checkPoolSetsNoRole(t, "the refused charge")
}

// checkPoolSetsNoRole holds every connection of the store's pool at once
// and checks that none has set a role, after what.
func checkPoolSetsNoRole(t *testing.T, after string) {
	t.Helper()
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
			t.Errorf("a pooled connection's role after %s = %q, want %q", after, role, roleNone)
		}
	}
}

// errConsumer is a callback's own failure.
var errConsumer = errors.New("the consumer's own failure")

// The SQL a callback ends the call's transaction with around a SET ROLE of
// the session, which no ROLLBACK of the store's reaches.
var roleAroundItsOwnEnd = map[string]string{
	"SET ROLE, COMMIT":        sqlSetRole + `; COMMIT`,
	"COMMIT, SET ROLE":        `COMMIT; ` + sqlSetRole,
	"SET ROLE, COMMIT, BEGIN": sqlSetRole + `; COMMIT; BEGIN`,
}

// A callback that sets the session's role around a COMMIT of its own, where
// no ROLLBACK reaches it, fails the call, and the connection it ran on goes
// back to no pool under that role: closed instead. That holds whether the
// callback returns nil or an error of its own.
func TestTxFromKeepsNoRoleSetAroundTheCallbacksOwnEnd(t *testing.T) {
	for name, sql := range roleAroundItsOwnEnd {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, failing %v", name, fail), func(t *testing.T) {
				f := newTenantFixture(t)
				ctx := WithTenant(t.Context(), f.mine)
				onCharged := func(ctx context.Context, _ meter.Charged) error {
					tx, _ := TxFrom(ctx)
					if _, err := tx.Exec(ctx, sql); err != nil {
						return err
					}
					if fail {
						return errConsumer
					}
					return nil
				}
				_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged)
				if err == nil {
					t.Error("a charge whose onCharged ended its transaction succeeded")
				}
				checkPoolSetsNoRole(t, "a charge whose onCharged set a role around its own COMMIT")
			})
		}
	}
}

// sqlSetLocalRole sets the role for the rest of the transaction, and
// sqlSetLocalRoleBack sets it back to none for the rest of it.
const (
	sqlSetLocalRole     = `SET LOCAL ROLE ` + appUser
	sqlSetLocalRoleBack = `SET LOCAL ROLE NONE`
)

// The store's statements after the callback run under the role the
// callback found: one that returns under another, set for the transaction
// alone (SET LOCAL ROLE) as much as for the session, is refused; one that
// sets the role for its own statements and back before it returns is not.
func TestTxFromRefusesACallbackReturningUnderALocalRole(t *testing.T) {
	for name, tc := range map[string]struct {
		sql     []string
		refused bool
	}{
		"left set":         {sql: []string{sqlSetLocalRole}, refused: true},
		"set back in time": {sql: []string{sqlSetLocalRole, sqlOne, sqlSetLocalRoleBack}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			onCharged := func(ctx context.Context, _ meter.Charged) error {
				tx, _ := TxFrom(ctx)
				for _, q := range tc.sql {
					if _, err := tx.Exec(ctx, q); err != nil {
						return err
					}
				}
				return nil
			}
			_, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, onCharged)
			if tc.refused != errors.Is(err, ErrChargeTransaction) || (!tc.refused && err != nil) {
				t.Errorf("a charge whose onCharged's role was %s: err = %v, want refused %v", name, err, tc.refused)
			}
		})
	}
}
