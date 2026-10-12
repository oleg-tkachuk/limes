package limes

import (
	"reflect"
	"testing"
)

// A meter request names its ceilings only through the subject: a field of
// its own would be one a caller could type, or leave at zero and so lift.
func TestMeterRequestsCarryNoCeilingOfTheirOwn(t *testing.T) {
	ceilings := []string{"CapabilityID", "TenantID", "MaxRequests", "MaxBudgetAmount", "UnitCode", "Copies"}
	for _, req := range []any{BumpRequest{}, ChargeRequest{}, ReserveRequest{}, SettleRequest{}} {
		typ := reflect.TypeOf(req)
		for _, name := range ceilings {
			if _, ok := typ.FieldByName(name); ok {
				t.Errorf("%s has a field %s outside its Subject", typ.Name(), name)
			}
		}
	}
}
