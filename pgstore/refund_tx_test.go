package pgstore

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

const (
	// sqlRefundedOf reads what the ledger records refunded of one charge.
	sqlRefundedOf = `SELECT refunded FROM ` + schema + `.charges WHERE id = $1`
	// sqlRefundCount counts the refunds rows of one refund id.
	sqlRefundCount = `SELECT count(*) FROM ` + schema + `.refunds WHERE id = $1`
)

// refundTxCharge is what the charge the refund tests refund takes, and
// refundTxPart what one refund of it returns.
const (
	refundTxCharge limes.Nanos = 3
	refundTxPart   limes.Nanos = 1
)

// chargeToRefund charges the fixture's child refundTxCharge and returns the
// charge.
func chargeToRefund(t *testing.T, ctx context.Context, f tenantFixture) meter.ChargeReceipt {
	t.Helper()
	c, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: refundTxCharge}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// onRefunded runs once the refund's writes are sent, before COMMIT, on the
// refund's own transaction: through TxFrom it reads the charge's refunded
// amount with this refund in it and, for a keyed refund, its refunds row.
func TestOnRefundedRunsAfterTheWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		refundID uuid.UUID
		rows     int
	}{
		{name: "unkeyed", refundID: uuid.Nil(), rows: 0},
		{name: "keyed", refundID: uuid.New(), rows: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			c := chargeToRefund(t, ctx, f)
			var refunded int64
			var rows int
			ran := false
			onRefunded := func(ctx context.Context, r meter.Refunded) error {
				ran = true
				tx, ok := TxFrom(ctx)
				if !ok {
					t.Fatal("onRefunded's context holds no transaction")
				}
				if err := tx.QueryRow(ctx, sqlRefundedOf, r.Receipt.ChargeID).Scan(&refunded); err != nil {
					return err
				}
				return tx.QueryRow(ctx, sqlRefundCount, r.Receipt.RefundID).Scan(&rows)
			}
			got, err := f.usage.Refund(ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: refundTxPart, RefundID: tc.refundID}, onRefunded)
			if err != nil || !ran {
				t.Fatalf("refund = %+v, %v, onRefunded ran %t", got, err, ran)
			}
			if refunded != int64(refundTxPart) || rows != tc.rows {
				t.Errorf("inside onRefunded: %d refunded and %d refunds rows, want %d and %d", refunded, rows, refundTxPart, tc.rows)
			}
		})
	}
}

// onRefunded may not end the refund's transaction: Commit and Rollback
// through TxFrom are refused, and the refund commits.
func TestOnRefundedKeepsTheTransactionTheStores(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	c := chargeToRefund(t, ctx, f)
	onRefunded := func(ctx context.Context, _ meter.Refunded) error {
		tx, _ := TxFrom(ctx)
		if err := tx.Commit(ctx); !errors.Is(err, ErrChargeTransaction) {
			t.Errorf("Commit in onRefunded: err = %v, want ErrChargeTransaction", err)
		}
		if err := tx.Rollback(ctx); !errors.Is(err, ErrChargeTransaction) {
			t.Errorf("Rollback in onRefunded: err = %v, want ErrChargeTransaction", err)
		}
		return nil
	}
	if _, err := f.usage.Refund(ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: refundTxPart}, onRefunded); err != nil {
		t.Fatalf("a refund whose onRefunded tried to end its transaction: %v", err)
	}
	if rec, err := f.usage.GetCharge(ctx, c.ChargeID); err != nil || rec.RefundedAmount != refundTxPart {
		t.Errorf("the charge after the refund = %+v, %v; want %v refunded", rec, err, refundTxPart)
	}
}

// SQL that ends the refund's transaction refuses the refund as it refuses a
// charge: its writes went before onRefunded, so a COMMIT of onRefunded's
// committed them and a ROLLBACK undid them, and the call says it does not
// know which.
func TestOnRefundedRefusesATransactionEndedBySQL(t *testing.T) {
	for _, tc := range []struct {
		sql      string
		refunded limes.Nanos
	}{
		{"COMMIT", refundTxPart},
		{"ROLLBACK", 0},
		{"COMMIT; BEGIN", refundTxPart},
		{"ROLLBACK; BEGIN", 0},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			c := chargeToRefund(t, ctx, f)
			onRefunded := func(ctx context.Context, _ meter.Refunded) error {
				tx, _ := TxFrom(ctx)
				_, _ = tx.Exec(ctx, tc.sql) // what it ended is the store's to find
				return nil
			}
			_, err := f.usage.Refund(ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: refundTxPart}, onRefunded)
			if !errors.Is(err, ErrChargeTransaction) || !errors.Is(err, meter.ErrOutcomeUnknown) {
				t.Errorf("a refund whose onRefunded ran %s: err = %v, want ErrChargeTransaction, outcome unknown", tc.sql, err)
			}
			if rec, err := f.usage.GetCharge(ctx, c.ChargeID); err != nil || rec.RefundedAmount != tc.refunded {
				t.Errorf("the charge after %s = %+v, %v; want %v refunded", tc.sql, rec, err, tc.refunded)
			}
		})
	}
}
