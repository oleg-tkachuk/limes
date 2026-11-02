package meter

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/optional"
)

// Feature names an optional interface a UsageStore may implement beside the
// ones it must. A caller reaches each by a type assertion, so a store, or a
// decorator over one, that leaves one out turns its feature off with no
// error until it is used; Require, As and AsAll refuse that at startup
// instead, and a store's FeatureGate says which its configuration turned
// off.
type Feature string

// The features.
const (
	// FeatureCopyUsage — CopyUsageReader: a Biscuit copy's own counters.
	FeatureCopyUsage Feature = "copy_usage"
	// FeatureTenantUsageErase — TenantUsageEraser: removing a tenant's usage.
	FeatureTenantUsageErase Feature = "tenant_usage_erase"
	// FeatureMatchingRelease — MatchingReleaser: releasing the holds a
	// revocation outdates.
	FeatureMatchingRelease Feature = "matching_release"
	// FeatureTenantReservations — TenantReservationLister: listing a
	// tenant's open reservations.
	FeatureTenantReservations Feature = "tenant_reservations"
	// FeatureLeases — Leaser: leasing part of a chain's limits.
	FeatureLeases Feature = "leases"
)

// FeatureGate is a UsageStore whose configuration can turn off a feature it
// implements — pgstore's leases, Config.DisableLeases — saying which are on.
// A decorator forwards it. A store that is no FeatureGate has every feature
// it implements on.
type FeatureGate interface {
	FeatureEnabled(f Feature) bool
}

// featureCheck is a feature and the interface a store implements it by.
type featureCheck struct {
	feature Feature
	iface   reflect.Type
}

// featureChecks is every feature, in a fixed order.
var featureChecks = []featureCheck{
	{FeatureCopyUsage, reflect.TypeFor[CopyUsageReader]()},
	{FeatureTenantUsageErase, reflect.TypeFor[TenantUsageEraser]()},
	{FeatureMatchingRelease, reflect.TypeFor[MatchingReleaser]()},
	{FeatureTenantReservations, reflect.TypeFor[TenantReservationLister]()},
	{FeatureLeases, reflect.TypeFor[Leaser]()},
}

// enabledBy is whether s's configuration has on the feature an interface
// serves: every interface no feature names, and every one of a store that
// is no FeatureGate, is.
func enabledBy(s UsageStore) optional.Enabled {
	gate, ok := s.(FeatureGate)
	if !ok {
		return nil
	}
	return func(iface reflect.Type) bool {
		i := slices.IndexFunc(featureChecks, func(c featureCheck) bool { return c.iface == iface })
		return i < 0 || gate.FeatureEnabled(featureChecks[i].feature)
	}
}

// AllFeatures returns every feature, in a fixed order.
func AllFeatures() []Feature {
	out := make([]Feature, len(featureChecks))
	for i, c := range featureChecks {
		out[i] = c.feature
	}
	return out
}

// Features returns the features s implements and its configuration has on
// (FeatureGate), in AllFeatures' order.
func Features(s UsageStore) []Feature {
	var out []Feature
	enabled := enabledBy(s)
	for _, c := range featureChecks {
		if optional.Absence(s, c.iface, enabled) == "" {
			out = append(out, c.feature)
		}
	}
	return out
}

// Require refuses, with limes.ErrInvalidConfig naming each feature missing
// or turned off, a store without every feature in want: a consumer calls it
// once at startup with the features it uses, so a store or decorator that
// dropped one fails there rather than on the first call that needs it.
func Require(s UsageStore, want ...Feature) error {
	for _, w := range want {
		if !slices.ContainsFunc(featureChecks, func(c featureCheck) bool { return c.feature == w }) {
			return invalidRequest("unknown feature %q", w)
		}
	}
	var missing []string
	enabled := enabledBy(s)
	for _, c := range featureChecks {
		if !slices.Contains(want, c.feature) {
			continue
		}
		if why := optional.Absence(s, c.iface, enabled); why != "" {
			missing = append(missing, fmt.Sprintf("%s: %s", c.feature, why))
		}
	}
	return lacking(missing)
}

// As is s as the optional interface T — meter.As[meter.Leaser](store) — or
// limes.ErrInvalidConfig naming T when s is not one, or its configuration
// turned T's feature off: Require and the type assertion after it, in one
// call.
func As[T any](s UsageStore) (T, error) {
	var t T
	if err := AsAll(s, &t); err != nil {
		return t, err
	}
	return t, nil
}

// AsAll sets each target, a pointer to an optional interface, to s, or
// refuses with limes.ErrInvalidConfig naming every one s is not or has
// turned off — one call at startup for every interface a consumer uses. A
// target that is no pointer to an interface is limes.ErrInvalidRequest, and
// none is set.
func AsAll(s UsageStore, targets ...any) error {
	missing, err := optional.Assign(s, enabledBy(s), limes.ErrInvalidRequest, targets...)
	if err != nil {
		return err
	}
	return lacking(missing)
}

// lacking is limes.ErrInvalidConfig naming why each of missing is, or nil
// for none.
func lacking(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the usage store lacks %s", limes.ErrInvalidConfig, strings.Join(missing, ", "))
}
