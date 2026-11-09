package meter_test

import (
	"reflect"
	"testing"

	"github.com/oleg-tkachuk/limes/meter"
)

// Every StoreOperation names a method of the store interfaces, so an event
// and the call it is about are spelled alike.
func TestStoreOperationsNameStoreMethods(t *testing.T) {
	ifaces := []reflect.Type{
		reflect.TypeFor[meter.UsageStore](),
		reflect.TypeFor[meter.Leaser](),
		reflect.TypeFor[meter.MatchingReleaser](),
	}
	ops := []meter.StoreOperation{
		meter.StoreOperationCharge, meter.StoreOperationReserve, meter.StoreOperationSettle,
		meter.StoreOperationRelease, meter.StoreOperationReleaseExpired, meter.StoreOperationReleaseMatching,
		meter.StoreOperationRefund, meter.StoreOperationLease,
		meter.StoreOperationSettleLease, meter.StoreOperationSetTenantBudget,
		meter.StoreOperationDeleteUsage, meter.StoreOperationPurgeOrphans,
	}
	for _, op := range ops {
		found := false
		for _, it := range ifaces {
			if _, ok := it.MethodByName(string(op)); ok {
				found = true
			}
		}
		if !found {
			t.Errorf("StoreOperation %q names no method of UsageStore, Leaser or MatchingReleaser", op)
		}
	}
}
