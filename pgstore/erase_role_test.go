package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// An onErased that sets the session's role has the erase refused and
// rolled back, the role with it: the tenant is not erased, its charges go
// on, and no pooled connection keeps the role.
func TestEraseTenantRefusesACallbacksRole(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	_, err := f.records.EraseTenant(ctx, limes.EraseTenantRequest{TenantID: f.mine}, func(ctx context.Context, e Erased) error {
		_, err := e.Tx.Exec(ctx, sqlSetRole)
		return err
	})
	if !errors.Is(err, ErrChargeTransaction) || errors.Is(err, meter.ErrOutcomeUnknown) {
		t.Errorf("an erase whose onErased set the role: err = %v, want ErrChargeTransaction, the outcome known", err)
	}
	if _, err := f.usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil); err != nil {
		t.Errorf("a charge after the refused erase: %v", err)
	}
	checkPoolSetsNoRole(t, "the refused erase")
}

// The SQL an onErased ends the erase's transaction with.
var endingTheErase = map[string]string{
	"ROLLBACK":        `ROLLBACK`,
	"ROLLBACK, BEGIN": `ROLLBACK; BEGIN`,
	"COMMIT":          `COMMIT`,
	"COMMIT, BEGIN":   `COMMIT; BEGIN`,
}

// An onErased that ends the erase's transaction with SQL of its own fails
// the erase as ErrChargeTransaction and meter.ErrOutcomeUnknown, never as a
// committed erase: which way the SQL went is not the store's to know.
func TestEraseTenantRefusesACallbackEndingItsTransaction(t *testing.T) {
	for name, sql := range endingTheErase {
		t.Run(name, func(t *testing.T) {
			f := newTenantFixture(t)
			ctx := WithTenant(t.Context(), f.mine)
			_, err := f.records.EraseTenant(ctx, limes.EraseTenantRequest{TenantID: f.mine}, func(ctx context.Context, e Erased) error {
				_, err := e.Tx.Exec(ctx, sql)
				return err
			})
			if !errors.Is(err, ErrChargeTransaction) || !errors.Is(err, meter.ErrOutcomeUnknown) {
				t.Errorf("an erase whose onErased sent %s: err = %v, want ErrChargeTransaction and ErrOutcomeUnknown", sql, err)
			}
		})
	}
}
