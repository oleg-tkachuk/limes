package limes_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
)

// bareRecords is a limes.Store and nothing more, as a decorator that
// forwards none of the optional interfaces is.
type bareRecords struct{ limes.Store }

// As and AsAll hand back the record store as the optional interfaces asked
// for, and refuse one that is not them with ErrInvalidConfig naming each.
func TestStoreAs(t *testing.T) {
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := limes.As[limes.RevocationLineageLookup](records)
	if err != nil || lineage == nil {
		t.Fatalf("As[RevocationLineageLookup](memstore) = %v, %v; want the store", lineage, err)
	}
	var (
		eraser  limes.TenantRecordEraser
		revoker limes.BulkRevoker
	)
	if err := limes.AsAll(records, &eraser, &revoker); err != nil || eraser == nil || revoker == nil {
		t.Fatalf("AsAll(memstore) = %v; set %v, %v", err, eraser, revoker)
	}
	bare := bareRecords{records}
	if got, err := limes.As[limes.RevocationLineageLookup](bare); !errors.Is(err, limes.ErrInvalidConfig) || got != nil {
		t.Errorf("As of a bare store = %v, %v; want ErrInvalidConfig", got, err)
	}
	eraser, revoker = nil, nil
	err = limes.AsAll(bare, &eraser, &revoker)
	if !errors.Is(err, limes.ErrInvalidConfig) || !strings.Contains(err.Error(), "limes.TenantRecordEraser") || !strings.Contains(err.Error(), "limes.BulkRevoker") {
		t.Errorf("AsAll of a bare store = %v, want ErrInvalidConfig naming both", err)
	}
	if err := limes.AsAll(records, eraser); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("AsAll with a target no pointer = %v, want ErrInvalidRequest", err)
	}
}
