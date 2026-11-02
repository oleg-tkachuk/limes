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

// gatedStore is memstore's usage store with its configuration turning
// leases off, as pgstore's Config.DisableLeases does.
type gatedStore struct {
	meter.UsageStore
	meter.Leaser
}

func (gatedStore) FeatureEnabled(f meter.Feature) bool { return f != meter.FeatureLeases }

// A feature a store implements and its configuration turned off is no
// feature: Features leaves it out, and Require, As and AsAll refuse it.
func TestAFeatureTurnedOffIsMissing(t *testing.T) {
	full := fullStore(t)
	gated := gatedStore{full, full.(meter.Leaser)}
	if _, ok := any(gated).(meter.Leaser); !ok {
		t.Fatal("the gated store is no Leaser: the test checks nothing")
	}
	if _, ok := any(gated).(meter.CopyUsageReader); ok {
		t.Fatal("the gated store is a CopyUsageReader: the test checks nothing")
	}
	if got := meter.Features(gated); slices.Contains(got, meter.FeatureLeases) {
		t.Errorf("Features = %v, names leases turned off", got)
	}
	if err := meter.Require(gated, meter.FeatureLeases); !errors.Is(err, meter.ErrFeatureDisabled) || !errors.Is(err, limes.ErrInvalidConfig) || !strings.Contains(err.Error(), "off by its configuration") {
		t.Errorf("Require(leases) = %v, want ErrFeatureDisabled saying leases are off", err)
	}
	if l, err := meter.As[meter.Leaser](gated); !errors.Is(err, meter.ErrFeatureDisabled) || l != nil {
		t.Errorf("As[Leaser] = %v, %v; want ErrFeatureDisabled", l, err)
	}
	// Turned off and not implemented are told apart, and both reported.
	var copies meter.CopyUsageReader
	var leaser meter.Leaser
	err := meter.AsAll(gated, &copies, &leaser)
	if !errors.Is(err, meter.ErrFeatureDisabled) || !strings.Contains(err.Error(), "meter.CopyUsageReader") {
		t.Errorf("AsAll of a store lacking one and turning off another = %v; want both named, ErrFeatureDisabled among them", err)
	}
	if _, err := meter.As[meter.CopyUsageReader](gated); errors.Is(err, meter.ErrFeatureDisabled) || !errors.Is(err, limes.ErrInvalidConfig) {
		t.Errorf("As of an interface not implemented = %v; want ErrInvalidConfig and not ErrFeatureDisabled", err)
	}
}

// AsAll sets every interface the store is, and names every other one in a
// single error; a target that is no pointer to an interface sets nothing.
func TestAsAll(t *testing.T) {
	var (
		copies  meter.CopyUsageReader
		leaser  meter.Leaser
		matcher meter.MatchingReleaser
	)
	if err := meter.AsAll(fullStore(t), &copies, &leaser, &matcher); err != nil || copies == nil || leaser == nil || matcher == nil {
		t.Fatalf("AsAll(memstore) = %v; set %v %v %v", err, copies, leaser, matcher)
	}
	copies, leaser, matcher = nil, nil, nil
	err := meter.AsAll(bareStore{fullStore(t)}, &copies, &leaser)
	if !errors.Is(err, limes.ErrInvalidConfig) || !strings.Contains(err.Error(), "meter.CopyUsageReader") || !strings.Contains(err.Error(), "meter.Leaser") {
		t.Fatalf("AsAll(bare) = %v, want ErrInvalidConfig naming both interfaces", err)
	}
	if err := meter.AsAll(fullStore(t), &leaser, leaser); !errors.Is(err, limes.ErrInvalidRequest) || leaser != nil {
		t.Errorf("AsAll with a target no pointer = %v, leaser %v; want ErrInvalidRequest and nothing set", err, leaser)
	}
}
