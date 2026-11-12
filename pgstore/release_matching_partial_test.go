package pgstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// failSettleMessage is what the test trigger of sqlFailLeaseSettle raises.
const failSettleMessage = "limes test: lease settlement refused"

// sqlFailLeaseSettle installs a trigger that refuses ending the lease of
// the capability %s: a lease settlement that fails after its hold sweep
// committed.
var sqlFailLeaseSettle = `
CREATE FUNCTION public.limes_test_fail_settle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF OLD.capability_id = '%s' AND OLD.lease_id IS NOT NULL AND NEW.lease_id IS NULL THEN
		RAISE EXCEPTION '` + failSettleMessage + `';
	END IF;
	RETURN NEW;
END $$;
CREATE TRIGGER limes_test_fail_settle BEFORE UPDATE ON ` + schema + `.usage
	FOR EACH ROW EXECUTE FUNCTION public.limes_test_fail_settle();`

// sqlDropFailLeaseSettle removes what sqlFailLeaseSettle installs.
const sqlDropFailLeaseSettle = `
DROP TRIGGER IF EXISTS limes_test_fail_settle ON ` + schema + `.usage;
DROP FUNCTION IF EXISTS public.limes_test_fail_settle();`

// A ReleaseMatching whose lease settlement fails after its holds were
// released and committed reports those holds with the error, as
// ReleaseExpired does: a caller told nothing was released would not know
// the holds are gone.
func TestReleaseMatchingReportsTheHoldsItReleasedBeforeAFailedSettlement(t *testing.T) {
	f := newTenantFixture(t)
	mine := WithTenant(context.Background(), f.mine)
	client := "client-" + uuid.New().String()
	record := func(parent uuid.UUID, clientID string) uuid.UUID {
		t.Helper()
		c := f.parent
		c.ID, c.ParentID, c.ClientID, c.Subject.Subject = uuid.New(), parent, clientID, "agent:sweep"
		if err := f.records.Insert(mine, limes.InsertRequest{Capability: c}); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	root := record(uuid.Nil(), client)
	held, leased := record(root, ""), record(root, "")
	if _, err := f.usage.Reserve(mine, meter.ReserveRequest{Subject: meterSubject(held, f.mine), Amount: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.usage.Lease(mine, meter.LeaseRequest{Subject: meterSubject(leased, f.mine), Amount: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.records.Revoke(mine, limes.RevokeRequest{CapabilityID: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), fmt.Sprintf(sqlFailLeaseSettle, leased)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), sqlDropFailLeaseSettle) })
	got, err := f.usage.ReleaseMatching(mine, meter.ReleaseMatchingRequest{TenantID: f.mine, ClientID: client})
	if err == nil || !strings.Contains(err.Error(), failSettleMessage) {
		t.Fatalf("ReleaseMatching returned %v, want the settlement's refusal", err)
	}
	if got.Released != 1 || got.SettledLeases != 0 || !slices.Equal(got.Tenants, []uuid.UUID{f.mine}) {
		t.Errorf("ReleaseMatching with a failed settlement = %+v, want the one hold it committed in %s", got, f.mine)
	}
	if usage, err := f.usage.GetUsage(mine, held); err != nil || usage.ReservedAmount != 0 {
		t.Errorf("the held capability after the sweep = %+v, %v; want nothing held", usage, err)
	}
}
