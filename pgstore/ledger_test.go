package pgstore

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// sqlLedgerCharge writes a charge straight into the ledger, as the owner:
// sums past MaxNanos take more charges than any check should make.
const sqlLedgerCharge = `
INSERT INTO ` + schema + `.charges
    (id, capability_id, tenant_id, amount, unit_code, op, actor, at, ancestors, copies, overrun, tenant_period)
VALUES ($1, $2, $3, $4, $5, '', '', $6, '{}', '{}', false, 0)`

// A sum past MaxNanos is refused as ErrCounterOverflow, not wrapped round or
// rounded: the database sums exactly, and so must what reads it.
func TestSumChargesRefusesASumPastMaxNanos(t *testing.T) {
	fresh(t)
	tenant, capID := uuid.New(), uuid.New()
	at := time.Now().UTC().Truncate(timeResolution)
	for range 2 {
		if _, err := owner.Exec(t.Context(), sqlLedgerCharge, uuid.New(), capID, tenant, int64(limes.MaxNanos), limes.DefaultUnitCode, at); err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := NewLedger(Config{DB: app})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.SumCharges(WithTenant(t.Context(), tenant), limes.SumChargesRequest{
		TenantID: tenant, From: at.Add(-time.Hour), To: at.Add(time.Hour),
	})
	if !errors.Is(err, limes.ErrCounterOverflow) {
		t.Errorf("SumCharges past MaxNanos: err = %v, want ErrCounterOverflow", err)
	}
}

// A ledger reader needs a database, as the stores do.
func TestNewLedgerRequiresADB(t *testing.T) {
	if _, err := NewLedger(Config{}); !errors.Is(err, errNoDB) {
		t.Errorf("NewLedger without a DB: err = %v, want errNoDB", err)
	}
}
