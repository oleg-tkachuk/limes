package metertest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/internal/watchdog"
	"github.com/oleg-tkachuk/limes/meter"
)

// CounterEnv is one meter.RequestCounter under test, fresh for each check.
// Its fields mean what Env's of the same name do.
type CounterEnv struct {
	// Ctx is the context every call for Tenant runs under.
	Ctx context.Context
	// Counter is the store under test, with no request counted.
	Counter meter.RequestCounter
	// Tenant is a tenant the store can count for.
	Tenant uuid.UUID
	// NewCapability records a capability of Tenant as spec describes it, so
	// the counter finds its lineage and its ceilings, and returns its id.
	// Its Budget is the unlimited one in the suite's unit.
	NewCapability func(spec CapabilitySpec) (uuid.UUID, error)
	// CheckTimeout bounds one check. Optional: without it,
	// DefaultCheckTimeout.
	CheckTimeout time.Duration
	// DeleteCapability removes a capability's record, as Store.Purge does.
	// Optional: without it, the checks of a capability or an ancestor no
	// longer on record are skipped.
	DeleteCapability func(id uuid.UUID) error
	// NewTenant creates another tenant. Optional: without it, a random id.
	NewTenant func() (uuid.UUID, error)
	// TenantCtx is the context a call for tenant runs under. Optional:
	// without it, Ctx.
	TenantCtx func(tenant uuid.UUID) context.Context
	// ScopesTenants reports that a Bump made under another tenant's context
	// is refused with limes.ErrScopeMismatch and counts nothing. Optional:
	// without it, that check is skipped.
	ScopesTenants bool
	// ReadOnlyCtx is a context scoped to reading and writing nothing, under
	// which a Bump is refused with limes.ErrScopeMismatch. Optional: without
	// it, that check is skipped.
	ReadOnlyCtx context.Context
}

// counterEnv is the CounterEnv of e's store.
func (e Env) counterEnv() CounterEnv {
	return CounterEnv{
		Ctx: e.Ctx, Counter: e.Usage, Tenant: e.Tenant, NewCapability: e.NewCapability,
		CheckTimeout: e.CheckTimeout, DeleteCapability: e.DeleteCapability, NewTenant: e.NewTenant,
		TenantCtx: e.TenantCtx, ScopesTenants: e.ScopesTenants, ReadOnlyCtx: e.ReadOnlyCtx,
	}
}

// RunCounter runs the checks of meter.RequestCounter as subtests, each on a
// fresh CounterEnv from setup. They read nothing but Bump's receipts and
// refusals, so a store that counts requests and nothing else passes them:
// a capability's own ceiling, an ancestor's from its record bounding its
// whole subtree, a chain at the deepest delegation, a Biscuit copy's
// ceiling, a refusal that counts nothing, concurrent bumps at a ceiling, the
// zero ceiling and subject, a capability not on record, and, where the
// CounterEnv enables them, an ancestor removed, another tenant's scope and a
// read-only one.
//
// Run runs these checks too, on its Env's store.
func RunCounter(t *testing.T, setup func(t *testing.T) CounterEnv) {
	t.Helper()
	checks := []watchdog.Check[CounterEnv]{
		{Name: "BumpCountsUpToItsOwnCeiling", Run: checkCounterOwnCeiling},
		{Name: "AnAncestorCeilingBoundsItsSubtree", Run: checkCounterAncestor},
		{Name: "AChainAtMaxDepthCountsOnItsTop", Run: checkCounterDeepChain},
		{Name: "ACopyCeilingBoundsItsCopyAlone", Run: checkCounterCopy},
		{Name: "ARefusedBumpCountsNothing", Run: checkCounterRefusalCountsNothing},
		{Name: "ConcurrentBumpsKeepTheCeiling", Run: checkCounterConcurrent},
		{Name: "AZeroCeilingAllowsNothing", Run: checkCounterZeroCeiling},
		{Name: "TheZeroSubjectIsRefused", Run: checkCounterZeroSubject},
		{Name: "ACapabilityNotOnRecordIsRefused", Run: checkCounterNotOnRecord},
		{Name: "AnAncestorNoLongerOnRecordBoundsNothing", Run: checkCounterAncestorRemoved},
		{Name: "ACallScopedToAnotherTenantIsRefused", Run: checkCounterScopeMismatch},
		{Name: "AReadOnlyScopeCountsNothing", Run: checkCounterReadOnly},
	}
	watchdog.RunChecks(t, setup, func(e CounterEnv) time.Duration { return e.CheckTimeout }, checks)
}

// counterRequestLimit is the request ceiling the counter checks count up to.
const counterRequestLimit = 2

// record records a capability of the CounterEnv's tenant under parent, with
// requests as its record's request ceiling: a store reads it from there for
// an ancestor, and from the subject for the capability it counts.
func (e CounterEnv) record(t *testing.T, parent uuid.UUID, requests limes.RequestCeiling) uuid.UUID {
	t.Helper()
	id, err := e.NewCapability(CapabilitySpec{Parent: parent, Requests: requests, Budget: unlimitedBudget})
	expect.Must(t, err == nil, "record a capability: %v", err)
	return id
}

// unlimitedRequests is a record's request ceiling when the check needs none.
var unlimitedRequests = limes.RequestCeiling{Unlimited: true}

// bump counts one request of spec's capability under ctx, in the
// CounterEnv's tenant unless spec names another.
func (e CounterEnv) bump(ctx context.Context, spec subjectSpec) (int64, error) {
	if spec.tenant == uuid.Nil() {
		spec.tenant = e.Tenant
	}
	r, err := e.Counter.Bump(ctx, meter.BumpRequest{Subject: newSubject(spec)})
	return r.RequestCount, err
}

// admitted holds a bump, what, to having been admitted with the
// capability's count at want. Each is a check of its own, so that a store
// that admits the bump at a wrong count fails as one that refuses it does.
func admitted(t *testing.T, what string, n int64, err error, want int64) {
	t.Helper()
	expect.Must(t, err == nil, "%s: %v, want it admitted", what, err)
	expect.That(t, n == want, "%s counted %d requests, want %d", what, n, want)
}

// refused holds a bump, what, to ErrRequestLimitExceeded, with the
// capability's count left at want.
func refused(t *testing.T, what string, n int64, err error, want int64) {
	t.Helper()
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "%s: err = %v, want ErrRequestLimitExceeded", what, err)
	expect.That(t, n == want, "%s left the count at %d, want %d", what, n, want)
}

func checkCounterOwnCeiling(t *testing.T, e CounterEnv) {
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	spec := subjectSpec{capID: id, requests: requestsOf(counterRequestLimit)}
	for want := int64(1); want <= counterRequestLimit; want++ {
		n, err := e.bump(e.Ctx, spec)
		admitted(t, "a bump within the ceiling", n, err, want)
	}
	for range counterRequestLimit {
		n, err := e.bump(e.Ctx, spec)
		refused(t, "a bump past the ceiling", n, err, counterRequestLimit)
	}
}

func checkCounterAncestor(t *testing.T, e CounterEnv) {
	root := e.record(t, uuid.Nil(), limes.RequestCeiling{Max: counterRequestLimit})
	children := []uuid.UUID{e.record(t, root, unlimitedRequests), e.record(t, root, unlimitedRequests)}
	for _, child := range children {
		n, err := e.bump(e.Ctx, subjectSpec{capID: child})
		admitted(t, "a child's first bump", n, err, 1)
	}
	for _, child := range children {
		_, err := e.bump(e.Ctx, subjectSpec{capID: child})
		expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past the root's ceiling, counted by its children together: err = %v, want ErrRequestLimitExceeded", err)
	}
}

func checkCounterDeepChain(t *testing.T, e CounterEnv) {
	top := e.record(t, uuid.Nil(), limes.RequestCeiling{Max: 1})
	leaf := top
	for range limes.MaxDelegationDepth {
		leaf = e.record(t, leaf, unlimitedRequests)
	}
	_, err := e.bump(e.Ctx, subjectSpec{capID: leaf})
	expect.Must(t, err == nil, "a bump %d delegations below the top: %v", limes.MaxDelegationDepth, err)
	_, err = e.bump(e.Ctx, subjectSpec{capID: leaf})
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past the top's ceiling, %d delegations up: err = %v, want ErrRequestLimitExceeded", limes.MaxDelegationDepth, err)
}

func checkCounterCopy(t *testing.T, e CounterEnv) {
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	copyOf := func(name string) []limes.CopyCeiling {
		return []limes.CopyCeiling{{RevocationID: []byte(name), Requests: &limes.RequestCeiling{Max: 1}}}
	}
	n, err := e.bump(e.Ctx, subjectSpec{capID: id, copies: copyOf("first")})
	admitted(t, "a copy's first bump", n, err, 1)
	_, err = e.bump(e.Ctx, subjectSpec{capID: id, copies: copyOf("first")})
	expect.That(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past the copy's ceiling: err = %v, want ErrRequestLimitExceeded", err)
	n, err = e.bump(e.Ctx, subjectSpec{capID: id, copies: copyOf("second")})
	admitted(t, "a sibling copy's bump, the capability counting both", n, err, 2)
}

func checkCounterRefusalCountsNothing(t *testing.T, e CounterEnv) {
	root := e.record(t, uuid.Nil(), limes.RequestCeiling{Max: 1})
	child := e.record(t, root, unlimitedRequests)
	frozen := []limes.CopyCeiling{{RevocationID: []byte("frozen"), Requests: &limes.RequestCeiling{}}}
	n, err := e.bump(e.Ctx, subjectSpec{capID: child, copies: frozen})
	refused(t, "a bump past a frozen copy", n, err, 0)
	n, err = e.bump(e.Ctx, subjectSpec{capID: child})
	admitted(t, "a bump after a refused one, the root's one request still free", n, err, 1)
}

func checkCounterConcurrent(t *testing.T, e CounterEnv) {
	root := e.record(t, uuid.Nil(), limes.RequestCeiling{Max: counterRequestLimit})
	id := e.record(t, root, unlimitedRequests)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
		failures []error
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			_, err := e.bump(e.Ctx, subjectSpec{capID: id})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				admitted++
			case !errors.Is(err, limes.ErrRequestLimitExceeded):
				failures = append(failures, err)
			}
		})
	}
	wg.Wait()
	expect.That(t, len(failures) == 0, "concurrent bumps failed: %v", failures)
	expect.That(t, admitted == counterRequestLimit, "%d of %d concurrent bumps were admitted, want %d", admitted, concurrentDeliveries, counterRequestLimit)
}

func checkCounterZeroCeiling(t *testing.T, e CounterEnv) {
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	n, err := e.bump(e.Ctx, subjectSpec{capID: id, requests: requestsOf(0)})
	refused(t, "a bump under the zero ceiling", n, err, 0)
}

func checkCounterZeroSubject(t *testing.T, e CounterEnv) {
	_, err := e.Counter.Bump(e.Ctx, meter.BumpRequest{})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Bump of the zero subject: err = %v, want ErrInvalidRequest", err)
}

func checkCounterNotOnRecord(t *testing.T, e CounterEnv) {
	_, err := e.bump(e.Ctx, subjectSpec{capID: uuid.New()})
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a bump of a capability never recorded: err = %v, want ErrNotFound", err)
	if e.DeleteCapability == nil {
		return
	}
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	_, err = e.bump(e.Ctx, subjectSpec{capID: id})
	expect.Must(t, err == nil, "%v", err)
	expect.Must(t, e.DeleteCapability(id) == nil, "delete the record")
	_, err = e.bump(e.Ctx, subjectSpec{capID: id})
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a bump of a capability no longer on record: err = %v, want ErrNotFound", err)
}

func checkCounterAncestorRemoved(t *testing.T, e CounterEnv) {
	if e.DeleteCapability == nil {
		t.Skip("CounterEnv.DeleteCapability not set")
	}
	root := e.record(t, uuid.Nil(), limes.RequestCeiling{Max: 1})
	child := e.record(t, root, unlimitedRequests)
	_, err := e.bump(e.Ctx, subjectSpec{capID: child})
	expect.Must(t, err == nil, "%v", err)
	_, err = e.bump(e.Ctx, subjectSpec{capID: child})
	expect.Must(t, errors.Is(err, limes.ErrRequestLimitExceeded), "a bump past the root's ceiling: err = %v, want ErrRequestLimitExceeded", err)
	expect.Must(t, e.DeleteCapability(root) == nil, "delete the root's record")
	n, err := e.bump(e.Ctx, subjectSpec{capID: child})
	admitted(t, "a bump once the root is removed", n, err, 2)
}

func checkCounterScopeMismatch(t *testing.T, e CounterEnv) {
	if !e.ScopesTenants {
		t.Skip("CounterEnv.ScopesTenants not set")
	}
	other := uuid.New()
	if e.NewTenant != nil {
		var err error
		other, err = e.NewTenant()
		expect.Must(t, err == nil, "NewTenant: %v", err)
	}
	wrong := e.Ctx
	if e.TenantCtx != nil {
		wrong = e.TenantCtx(other)
	}
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	_, err := e.bump(wrong, subjectSpec{capID: id})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "a bump under another tenant's scope: err = %v, want ErrScopeMismatch", err)
	n, err := e.bump(e.Ctx, subjectSpec{capID: id})
	admitted(t, "a bump after one refused for its scope", n, err, 1)
}

func checkCounterReadOnly(t *testing.T, e CounterEnv) {
	if e.ReadOnlyCtx == nil {
		t.Skip("CounterEnv.ReadOnlyCtx not set")
	}
	id := e.record(t, uuid.Nil(), unlimitedRequests)
	_, err := e.bump(e.ReadOnlyCtx, subjectSpec{capID: id})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "a bump under a read-only scope: err = %v, want ErrScopeMismatch", err)
	n, err := e.bump(e.Ctx, subjectSpec{capID: id})
	admitted(t, "a bump after one refused as read-only", n, err, 1)
}
