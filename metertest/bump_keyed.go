package metertest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// bumpRef is the ExternalRef the keyed-bump checks count under.
const bumpRef = "request-1"

// errBumpSideEffect is what a failing onBumped returns.
var errBumpSideEffect = errors.New("side effect failed")

// failingBump is an onBumped that fails.
func failingBump(context.Context, meter.Bumped) error { return errBumpSideEffect }

// keyedBump counts one request of spec's capability under ctx and ref, in
// the CounterEnv's tenant unless spec names another, with onBumped.
func (e CounterEnv) keyedBump(ctx context.Context, spec subjectSpec, ref string, onBumped func(context.Context, meter.Bumped) error) (meter.BumpReceipt, error) {
	if spec.tenant == uuid.Nil() {
		spec.tenant = e.Tenant
	}
	return e.Counter.Bump(ctx, meter.BumpRequest{Subject: newSubject(spec), ExternalRef: ref}, onBumped)
}

// counted holds a keyed bump, what, to having counted afresh, at want.
func counted(t *testing.T, what string, r meter.BumpReceipt, err error, want int64) {
	t.Helper()
	expect.Must(t, err == nil, "%s: %v, want it counted", what, err)
	expect.That(t, !r.Replayed && r.RequestCount == want, "%s = %+v, want a fresh count of %d", what, r, want)
}

// replayed holds a keyed bump, what, to having replayed a first count of
// want.
func replayed(t *testing.T, what string, r meter.BumpReceipt, err error, want int64) {
	t.Helper()
	expect.Must(t, err == nil, "%s: %v, want a replay", what, err)
	expect.That(t, r.Replayed && r.RequestCount == want, "%s = %+v, want the first count of %d, replayed", what, r, want)
}

// A bump under an ExternalRef counts once: again, it is the first receipt,
// replayed, and counts nothing. The ref is the capability's, and no ref
// never deduplicates.
func checkCounterKeyedBumpCountsOnce(t *testing.T, e CounterEnv) {
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, nil)
	counted(t, "a keyed bump", r, err, 1)
	r, err = e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, nil)
	replayed(t, "the keyed bump again", r, err, 1)
	for want := int64(2); want <= 3; want++ {
		n, err := e.bump(e.Ctx, subjectSpec{capID: id})
		admitted(t, "a bump with no ref after a replay", n, err, want)
	}
	other := e.record(t, uuid.Nil(), unlimitedRequests)
	r, err = e.keyedBump(e.Ctx, subjectSpec{capID: other}, bumpRef, nil)
	counted(t, "the same ref on another capability", r, err, 1)
}

// conflictCopyRequests is the request ceiling of the copy
// checkCounterExternalRefNamesOneRequest bumps under: one, so a copy the
// conflict counted refuses the bump after it.
const conflictCopyRequests = 1

// An ExternalRef names one request: sent again under a Biscuit copy, or for
// another tenant, a bump is ErrExternalRefConflict, a conflict, and counts
// nothing — on the capability, on its ancestors or on the copy.
func checkCounterExternalRefNamesOneRequest(t *testing.T, e CounterEnv) {
	root := e.record(t, uuid.Nil(), limes.RequestCeiling{Max: counterRequestLimit})
	id := e.record(t, root, unlimitedRequests)
	r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, nil)
	counted(t, "a keyed bump", r, err, 1)
	another := []limes.CopyCeiling{{RevocationID: []byte("another-copy"), Requests: &limes.RequestCeiling{Max: conflictCopyRequests}}}
	other := uuid.New()
	if e.NewTenant != nil {
		other, err = e.NewTenant()
		expect.Must(t, err == nil, "NewTenant: %v", err)
	}
	cases := map[string]subjectSpec{
		"under a Biscuit copy": {capID: id, copies: another},
		"for another tenant":   {capID: id, tenant: other},
	}
	// A store that counts no copy cannot tell one from the capability.
	if e.NoCopies {
		delete(cases, "under a Biscuit copy")
	}
	for name, spec := range cases {
		want := meter.ErrExternalRefConflict
		// A store that scopes tenants refuses the call before it reads the
		// ref: Ctx is not that tenant's.
		if e.ScopesTenants && spec.tenant != uuid.Nil() {
			want = limes.ErrScopeMismatch
		}
		r, err := e.keyedBump(e.Ctx, spec, bumpRef, nil)
		expect.That(t, errors.Is(err, want), "the ref again %s = %+v, %v; want %v", name, r, err, want)
		expect.That(t, !errors.Is(want, meter.ErrExternalRefConflict) || errors.Is(err, limes.ErrConflict), "the ref again %s: %v, want a conflict", name, err)
		// The conflict says which ref, not whose the first bump was.
		expect.That(t, err == nil || !strings.Contains(err.Error(), e.Tenant.String()), "the ref again %s: the error names the first bump's tenant: %v", name, err)
	}
	if e.NoCopies {
		return
	}
	n, err := e.bump(e.Ctx, subjectSpec{capID: id, copies: another})
	admitted(t, "a bump under the copy after the conflicts", n, err, counterRequestLimit)
}

// An error from onBumped is returned as it is and counts nothing: not on
// the capability, not on an ancestor, and not under the ref, which counts
// afresh after it.
func checkCounterFailedBumpCallbackCountsNothing(t *testing.T, e CounterEnv) {
	root := e.record(t, uuid.Nil(), limes.RequestCeiling{Max: 1})
	id := e.record(t, root, unlimitedRequests)
	for _, ref := range []string{bumpRef, ""} {
		r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, ref, failingBump)
		expect.Must(t, errors.Is(err, errBumpSideEffect), "a bump under ref %q whose callback failed = %+v, %v; want the callback's error", ref, r, err)
		expect.That(t, r.RequestCount == 0 && !r.Replayed, "a bump under ref %q whose callback failed = %+v, want the count before it, 0", ref, r)
	}
	r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, nil)
	counted(t, "the ref after its callback failed, the root's one request still free", r, err, 1)
}

// onBumped runs once, before the call returns, and is handed the receipt
// the call returns.
func checkCounterBumpCallbackSeesTheReceipt(t *testing.T, e CounterEnv) {
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	var seen []meter.BumpReceipt
	onBumped := func(_ context.Context, b meter.Bumped) error {
		seen = append(seen, b.Receipt)
		return nil
	}
	for want, ref := range []string{bumpRef, ""} {
		r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, ref, onBumped)
		counted(t, "a bump with a callback", r, err, int64(want+1))
		expect.Must(t, len(seen) == want+1, "onBumped ran %d times over %d bumps, want once each", len(seen), want+1)
		got := seen[want]
		expect.That(t, got.RequestCount == r.RequestCount && !got.Replayed, "onBumped saw %+v, the call returned %+v", got, r)
	}
}

// A replay counts nothing, so it runs no onBumped.
func checkCounterReplayedBumpRunsNoCallback(t *testing.T, e CounterEnv) {
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, nil)
	counted(t, "a keyed bump", r, err, 1)
	runs := 0
	r, err = e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, func(context.Context, meter.Bumped) error { runs++; return nil })
	replayed(t, "the keyed bump again", r, err, 1)
	expect.That(t, runs == 0, "a replay ran onBumped %d times, want none", runs)
}

// A ref ValidateExternalRef refuses is ErrInvalidRequest and counts
// nothing; one of MaxExternalRefBytes counts.
func checkCounterInvalidExternalRefIsRefused(t *testing.T, e CounterEnv) {
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	for name, ref := range map[string]string{
		"too long":          strings.Repeat("r", meter.MaxExternalRefBytes+1),
		"holding a NUL":     "request\x00",
		"not printable":     "request\n",
		"not UTF-8":         "request\xff",
		"a control at once": "\x7f",
	} {
		r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, ref, nil)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "a bump under a ref %s = %+v, %v; want ErrInvalidRequest", name, r, err)
	}
	r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, strings.Repeat("r", meter.MaxExternalRefBytes), nil)
	counted(t, "a bump under a ref of the longest", r, err, 1)
}

// A replay is answered once the capability is no longer on record, as a
// charge's is: it counts nothing, so it needs no record. A bump under no
// ref is refused.
func checkCounterKeyedBumpReplaysOnceItsCapabilityIsGone(t *testing.T, e CounterEnv) {
	if e.DeleteCapability == nil {
		t.Skip("CounterEnv.DeleteCapability not set")
	}
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, nil)
	counted(t, "a keyed bump", r, err, 1)
	expect.Must(t, e.DeleteCapability(id) == nil, "delete the record")
	r, err = e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, nil)
	replayed(t, "the keyed bump once its capability is gone", r, err, 1)
	_, err = e.bump(e.Ctx, subjectSpec{capID: id})
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a bump with no ref once its capability is gone: err = %v, want ErrNotFound", err)
}

// Deliveries of one ref at once count it once: one is counted, every other
// replays it.
func checkCounterKeyedBumpsCountOnceConcurrently(t *testing.T, e CounterEnv) {
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		fresh    int
		failures []error
		counts   = map[int64]int{}
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			r, err := e.keyedBump(e.Ctx, subjectSpec{capID: id}, bumpRef, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			if !r.Replayed {
				fresh++
			}
			counts[r.RequestCount]++
		})
	}
	wg.Wait()
	expect.Must(t, len(failures) == 0, "concurrent deliveries of one ref failed: %v", failures)
	expect.That(t, fresh == 1, "%d of %d concurrent deliveries of one ref counted, want 1", fresh, concurrentDeliveries)
	expect.That(t, counts[1] == concurrentDeliveries, "concurrent deliveries of one ref returned counts %v, want each the first, 1", counts)
	n, err := e.bump(e.Ctx, subjectSpec{capID: id})
	admitted(t, "a bump with no ref after them", n, err, 2)
}

// A keyed bump goes with its capability's counters: once DeleteUsage
// restarts them, the ref counts afresh. PurgeOrphans, which drops the
// counters of a capability no longer on record, keeps it to replay.
func checkKeyedBumpGoesWithItsCounters(t *testing.T, f fixture) {
	child := f.childSubject()
	r, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: child, ExternalRef: bumpRef}, nil)
	counted(t, "a keyed bump", r, err, 1)
	expect.Must(t, f.Usage.DeleteUsage(f.Ctx, meter.DeleteUsageRequest{CapabilityID: f.child}) == nil, "DeleteUsage")
	r, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: child, ExternalRef: bumpRef}, nil)
	counted(t, "the ref once DeleteUsage restarted the counters", r, err, 1)
	if f.DeleteCapability == nil {
		return
	}
	gone, err := f.newCapability(uuid.Nil(), unlimitedBudget)
	expect.Must(t, err == nil, "%v", err)
	subject := newSubject(subjectSpec{capID: gone, tenant: f.Tenant})
	r, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: subject, ExternalRef: bumpRef}, nil)
	counted(t, "a keyed bump of a capability to remove", r, err, 1)
	expect.Must(t, f.DeleteCapability(gone) == nil, "delete the record")
	_, err = f.Usage.PurgeOrphans(f.Ctx, meter.PurgeOrphansRequest{})
	expect.Must(t, err == nil, "PurgeOrphans: %v", err)
	r, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: subject, ExternalRef: bumpRef}, nil)
	replayed(t, "the ref once PurgeOrphans dropped its counters", r, err, 1)
}

// A keyed bump is kept as long as a charge is: PurgeCharges keeps it within
// the retention and removes it past it, after the charges due and within the
// same limit, and the ref then counts afresh.
func checkKeyedBumpKeepsTheRetention(t *testing.T, f fixture) {
	housekeeping, ok := f.Ledger.(meter.LedgerHousekeeping)
	if !ok || f.Advance == nil {
		t.Skip("Env.Ledger is no meter.LedgerHousekeeping, or Env.Advance is not set")
	}
	child := meter.BumpRequest{Subject: f.childSubject(), ExternalRef: bumpRef}
	r, err := f.Usage.Bump(f.Ctx, child, nil)
	counted(t, "a keyed bump", r, err, 1)
	charged, err := f.charge(meter.ChargeRequest{Amount: one}, nil)
	expect.Must(t, err == nil, "%v", err)
	f.Advance(meter.MinChargeRetention - purgeStep)
	expect.Must(t, purgeCharges(t, f, housekeeping, meter.PurgeChargesRequest{}) == 0, "PurgeCharges within the retention removed something")
	r, err = f.Usage.Bump(f.Ctx, child, nil)
	replayed(t, "the ref within the retention", r, err, 1)
	f.Advance(2 * purgeStep)
	n := purgeCharges(t, f, housekeeping, meter.PurgeChargesRequest{Limit: 1})
	expect.Must(t, n == 1, "PurgeCharges of one past the retention removed %d, want 1", n)
	chargeKept(t, f, "the charge due beside the keyed bump", charged.ChargeID, false)
	r, err = f.Usage.Bump(f.Ctx, child, nil)
	replayed(t, "the ref once a batch of one took the charge first", r, err, 1)
	n = purgeCharges(t, f, housekeeping, meter.PurgeChargesRequest{Limit: 1})
	expect.Must(t, n == 1, "PurgeCharges of one after the charge removed %d, want the keyed bump", n)
	n = purgeCharges(t, f, housekeeping, meter.PurgeChargesRequest{})
	expect.That(t, n == 0, "PurgeCharges once nothing is due removed %d", n)
	r, err = f.Usage.Bump(f.Ctx, child, nil)
	counted(t, "the ref past the retention", r, err, 2)
}

// A keyed bump goes with its tenant's usage: once EraseTenantUsage removes
// it, the ref of the same capability under another tenant counts afresh,
// where before the erase it was ErrExternalRefConflict. A store that scopes
// tenants shows another tenant none of the capability, so the check is
// skipped there.
func checkKeyedBumpGoesWithItsTenantsUsage(t *testing.T, f fixture) {
	eraser, ok := f.Usage.(meter.TenantUsageEraser)
	if !ok {
		t.Skip("the UsageStore does not implement meter.TenantUsageEraser")
	}
	if f.ScopesTenants {
		t.Skip("Env.ScopesTenants is set: another tenant cannot meter the capability")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	theirs := meter.BumpRequest{Subject: newSubject(subjectSpec{capID: f.child, tenant: other}), ExternalRef: bumpRef}
	r, err := f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: f.childSubject(), ExternalRef: bumpRef}, nil)
	counted(t, "a keyed bump", r, err, 1)
	_, err = f.Usage.Bump(f.Ctx, theirs, nil)
	expect.Must(t, errors.Is(err, meter.ErrExternalRefConflict), "the ref for another tenant before the erase: err = %v, want ErrExternalRefConflict", err)
	_, err = eraser.EraseTenantUsage(f.Ctx, meter.EraseTenantUsageRequest{TenantID: f.Tenant})
	expect.Must(t, err == nil, "EraseTenantUsage: %v", err)
	r, err = f.Usage.Bump(f.Ctx, theirs, nil)
	counted(t, "the ref for another tenant after the erase", r, err, 1)
}

// Under a lease of requests a keyed bump counts once and replays as one
// not under it, and onBumped runs as it does there: handed the receipt,
// its error counting nothing on the capability or the lease.
func checkALeasedBumpIsKeyedAndRunsOnBumped(t *testing.T, f fixture) {
	l := f.leaser(t)
	c := f.newRequestChain(t)
	child := f.requestSubject(c.child, requestChild)
	f.leaseRequests(t, l, child, requestLeased)
	var seen []meter.BumpReceipt
	onBumped := func(_ context.Context, b meter.Bumped) error {
		seen = append(seen, b.Receipt)
		return nil
	}
	keyed := meter.BumpRequest{Subject: child, ExternalRef: bumpRef}
	r, err := f.Usage.Bump(f.Ctx, keyed, onBumped)
	counted(t, "a keyed bump under the lease", r, err, 1)
	expect.That(t, len(seen) == 1 && seen[0].RequestCount == r.RequestCount && !seen[0].Replayed, "onBumped under the lease saw %+v, the call returned %+v", seen, r)
	r, err = f.Usage.Bump(f.Ctx, keyed, onBumped)
	replayed(t, "the keyed bump under the lease again", r, err, 1)
	expect.That(t, len(seen) == 1, "a replay under the lease ran onBumped: %+v", seen)
	_, err = f.Usage.Bump(f.Ctx, meter.BumpRequest{Subject: child}, failingBump)
	expect.That(t, errors.Is(err, errBumpSideEffect), "a bump under the lease whose callback failed: err = %v, want the callback's", err)
	open, err := l.GetLease(f.Ctx, c.child)
	expect.That(t, err == nil && open.RequestsSpent == 1, "the lease after a failed callback = %+v, %v; want 1 request made", open, err)
	expect.That(t, f.usageOf(t, c.child).RequestCount == 1, "the child counted %d requests after a failed callback, want 1", f.usageOf(t, c.child).RequestCount)
}
