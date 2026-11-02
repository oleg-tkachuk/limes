package meter

import (
	"fmt"
	"slices"
	"strings"

	"github.com/oleg-tkachuk/limes"
)

// Feature names an optional interface a UsageStore may implement beside the
// ones it must. A caller reaches each by a type assertion, so a store, or a
// decorator over one, that leaves one out turns its feature off with no
// error until it is used; Require refuses that at startup instead.
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

// featureCheck is a feature and whether a store implements it.
type featureCheck struct {
	feature Feature
	has     func(UsageStore) bool
}

// featureChecks is every feature, in a fixed order.
var featureChecks = []featureCheck{
	{FeatureCopyUsage, func(s UsageStore) bool { _, ok := s.(CopyUsageReader); return ok }},
	{FeatureTenantUsageErase, func(s UsageStore) bool { _, ok := s.(TenantUsageEraser); return ok }},
	{FeatureMatchingRelease, func(s UsageStore) bool { _, ok := s.(MatchingReleaser); return ok }},
	{FeatureTenantReservations, func(s UsageStore) bool { _, ok := s.(TenantReservationLister); return ok }},
	{FeatureLeases, func(s UsageStore) bool { _, ok := s.(Leaser); return ok }},
}

// AllFeatures returns every feature, in a fixed order.
func AllFeatures() []Feature {
	out := make([]Feature, len(featureChecks))
	for i, c := range featureChecks {
		out[i] = c.feature
	}
	return out
}

// Features returns the features s implements, in AllFeatures' order. A
// store that implements an interface can still refuse its calls by
// configuration — pgstore's leases, ErrLeasingDisabled — which this does
// not see.
func Features(s UsageStore) []Feature {
	var out []Feature
	for _, c := range featureChecks {
		if c.has(s) {
			out = append(out, c.feature)
		}
	}
	return out
}

// Require refuses, with limes.ErrInvalidConfig naming what is missing, a
// store that does not implement every feature in want: a consumer calls it
// once at startup with the features it uses, so a store or decorator that
// dropped one fails there rather than on the first call that needs it.
func Require(s UsageStore, want ...Feature) error {
	for _, w := range want {
		if !slices.ContainsFunc(featureChecks, func(c featureCheck) bool { return c.feature == w }) {
			return invalidRequest("unknown feature %q", w)
		}
	}
	var missing []string
	for _, c := range featureChecks {
		if slices.Contains(want, c.feature) && !c.has(s) {
			missing = append(missing, string(c.feature))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: the usage store lacks %s", limes.ErrInvalidConfig, strings.Join(missing, ", "))
	}
	return nil
}
