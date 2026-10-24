package meter

import (
	"testing"
	"time"
)

// chargeRetentionDays is MinChargeRetention as ADR-0002 decided it: a year
// of billing and a month of disputes.
const chargeRetentionDays = 400

// The floor of a ledger's retention is the one the ADR decided, so a change
// of it is a decision too, not an edit.
func TestMinChargeRetentionIsTheDecidedFloor(t *testing.T) {
	if want := chargeRetentionDays * 24 * time.Hour; MinChargeRetention != want {
		t.Errorf("MinChargeRetention = %s, want %s", MinChargeRetention, want)
	}
}
