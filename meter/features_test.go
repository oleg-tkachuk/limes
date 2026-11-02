package meter_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
)

// fullStore is memstore's usage store, which implements every feature.
func fullStore(t *testing.T) meter.UsageStore {
	t.Helper()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

// bareStore is a UsageStore and nothing more, as a decorator that forwards
// none of the optional interfaces is.
type bareStore struct{ meter.UsageStore }

// Features names every optional interface a store implements, and none it
// does not: memstore implements them all, a bare UsageStore none.
func TestFeatures(t *testing.T) {
	full := fullStore(t)
	if got := meter.Features(full); !slices.Equal(got, meter.AllFeatures()) {
		t.Errorf("Features(memstore) = %v, want %v", got, meter.AllFeatures())
	}
	if got := meter.Features(bareStore{full}); len(got) != 0 {
		t.Errorf("Features of a bare UsageStore = %v, want none", got)
	}
}

// Require passes a store with every feature asked for, and refuses one that
// lacks any as limes.ErrInvalidConfig naming each missing feature; a
// feature it does not know is the caller's mistake, ErrInvalidRequest.
func TestRequire(t *testing.T) {
	full := fullStore(t)
	bare := bareStore{full}
	if err := meter.Require(full, meter.AllFeatures()...); err != nil {
		t.Errorf("Require(memstore, all) = %v, want nil", err)
	}
	if err := meter.Require(bare); err != nil {
		t.Errorf("Require(bare) asking for nothing = %v, want nil", err)
	}
	err := meter.Require(bare, meter.FeatureLeases, meter.FeatureCopyUsage)
	if !errors.Is(err, limes.ErrInvalidConfig) {
		t.Fatalf("Require(bare, leases, copy usage) = %v, want ErrInvalidConfig", err)
	}
	for _, f := range []meter.Feature{meter.FeatureLeases, meter.FeatureCopyUsage} {
		if !strings.Contains(err.Error(), string(f)) {
			t.Errorf("Require's error %q does not name %s", err, f)
		}
	}
	if strings.Contains(err.Error(), string(meter.FeatureMatchingRelease)) {
		t.Errorf("Require's error %q names a feature not asked for", err)
	}
	if err := meter.Require(full, meter.Feature("teleport")); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("Require of an unknown feature = %v, want ErrInvalidRequest", err)
	}
}

// As hands back the store as the interface asked for, and refuses one that
// is not it as limes.ErrInvalidConfig naming the interface.
func TestAs(t *testing.T) {
	full := fullStore(t)
	leaser, err := meter.As[meter.Leaser](full)
	if err != nil || leaser == nil {
		t.Fatalf("As[Leaser](memstore) = %v, %v; want the store", leaser, err)
	}
	if _, ok := leaser.(meter.UsageStore); !ok {
		t.Errorf("As[Leaser](memstore) is not the store it was given")
	}
	got, err := meter.As[meter.Leaser](bareStore{full})
	if !errors.Is(err, limes.ErrInvalidConfig) || got != nil {
		t.Fatalf("As[Leaser](bare) = %v, %v; want nil, ErrInvalidConfig", got, err)
	}
	if !strings.Contains(err.Error(), "meter.Leaser") {
		t.Errorf("As's error %q does not name meter.Leaser", err)
	}
}
