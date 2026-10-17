package limes

import (
	"reflect"
	"testing"
)

// The structs that may grow in 1.x are not comparable, so adding a slice or
// a map to one breaks no caller's ==; the ceilings, values a caller compares,
// stay comparable.
func TestWhichStructsCompare(t *testing.T) {
	for _, ty := range []reflect.Type{
		reflect.TypeFor[ChargeRecord](), reflect.TypeFor[ChargeReceipt](), reflect.TypeFor[RefundReceipt](),
		reflect.TypeFor[BumpReceipt](), reflect.TypeFor[TenantBudget](), reflect.TypeFor[Usage](),
		reflect.TypeFor[Revocation](), reflect.TypeFor[Problem](), reflect.TypeFor[Principal](),
		reflect.TypeFor[AgentPrincipal](), reflect.TypeFor[PurgeResult](), reflect.TypeFor[PurgeOrphansResult](),
		reflect.TypeFor[ReleaseExpiredResult](), reflect.TypeFor[EraseTenantResult](), reflect.TypeFor[RecordCount](),
		reflect.TypeFor[ChargeSum](),
	} {
		if ty.Comparable() {
			t.Errorf("%s is comparable; a field added in 1.x could break a caller's ==", ty.Name())
		}
	}
	for _, ty := range []reflect.Type{reflect.TypeFor[RequestCeiling](), reflect.TypeFor[BudgetCeiling]()} {
		if !ty.Comparable() {
			t.Errorf("%s is not comparable; callers compare ceilings", ty.Name())
		}
	}
}
