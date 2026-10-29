package metertest_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
	"github.com/oleg-tkachuk/limes/metertest"
)

// RunCounter is a contract suite: what it is worth is the faulty counters
// it fails. Each fault below runs the suite in a child test process, which
// must fail, at the check named; an honest counter, scoped or not, passes.

// faultEnv names the fault a child process runs the suite against.
const faultEnv = "LIMES_METERTEST_COUNTER_FAULT"

// counterTTL is how long the suite's capabilities live.
const counterTTL = time.Hour

// scopeKey marks a context with the tenant it is scoped to, and
// readOnlyKey one scoped to read every tenant and write none.
type (
	scopeKey    struct{}
	readOnlyKey struct{}
)

// counter is a memstore usage store seen as a meter.RequestCounter, with
// the scope a scoping store keeps, and a fault, if any, laid over it.
type counter struct {
	inner  meter.RequestCounter
	scoped bool
	fault  string
	mu     sync.Mutex
	// tenants are the tenants the store knows: its own and NewTenant's.
	tenants map[uuid.UUID]bool
}

// The faults, by what the counter gets wrong.
const (
	faultMiscounts        = "reports one request too many"
	faultMiscountsRefusal = "reports a refused request as counted"
	faultHidesRefusal     = "answers a refusal as admitted"
	faultIgnoresScope     = "ignores the call's scope"
	faultCountsRefused    = "counts a call it refuses for its scope"
)

func (c *counter) Bump(ctx context.Context, req meter.BumpRequest) (meter.BumpReceipt, error) {
	// A store refuses a malformed request before anything else (meter.Meter).
	if err := req.Validate(); err != nil {
		return meter.BumpReceipt{}, err
	}
	if c.scoped {
		if err := c.admitScope(ctx, req); err != nil {
			if c.fault == faultCountsRefused {
				_, _ = c.inner.Bump(ctx, req)
			}
			if c.fault != faultIgnoresScope {
				return meter.BumpReceipt{}, err
			}
		}
	}
	r, err := c.inner.Bump(ctx, req)
	switch {
	case c.fault == faultMiscounts && err == nil:
		return receipt(r.RequestCount + 1), nil
	case c.fault == faultMiscountsRefusal && err != nil:
		return receipt(r.RequestCount + 1), err
	case c.fault == faultHidesRefusal && errors.Is(err, limes.ErrRequestLimitExceeded):
		return r, nil
	}
	return r, err
}

// receipt is a receipt counting n requests.
func receipt(n int64) meter.BumpReceipt {
	var r meter.BumpReceipt
	r.RequestCount = n
	return r
}

// admitScope refuses a call scoped read only, or to another tenant than
// the request's, and one scoped to a tenant the store does not know.
func (c *counter) admitScope(ctx context.Context, req meter.BumpRequest) error {
	if ctx.Value(readOnlyKey{}) != nil {
		return limes.ErrScopeMismatch
	}
	tenant, ok := ctx.Value(scopeKey{}).(uuid.UUID)
	if !ok {
		return nil
	}
	c.mu.Lock()
	known := c.tenants[tenant]
	c.mu.Unlock()
	switch {
	case !known:
		return fmt.Errorf("%w: %s", limes.ErrUnknownTenant, tenant)
	case tenant != req.Subject.TenantID():
		return limes.ErrScopeMismatch
	}
	return nil
}

// counterEnv is a CounterEnv over a fresh memstore, scoped when scoped,
// with fault laid over its counter.
func counterEnv(t *testing.T, scoped bool, fault string) metertest.CounterEnv {
	t.Helper()
	records, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	c := &counter{inner: usage, scoped: scoped, fault: fault, tenants: map[uuid.UUID]bool{tenant: true}}
	expiry := time.Now().Add(counterTTL)
	env := metertest.CounterEnv{
		Ctx:     context.WithValue(t.Context(), scopeKey{}, tenant),
		Counter: c,
		Tenant:  tenant,
		NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
			cap := limes.Capability{
				ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: expiry,
				Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: cmp.Or(spec.Tenant, tenant), Subject: "agent"},
				Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: spec.Requests, Budget: spec.Budget, Copies: spec.CopyQuota()},
			}
			return cap.ID, records.Insert(context.Background(), limes.InsertRequest{Capability: cap, IssuedBy: limes.Principal{Subject: "issuer"}})
		},
	}
	if scoped {
		env.ScopesTenants = true
		env.ReadOnlyCtx = context.WithValue(t.Context(), readOnlyKey{}, true)
		env.NewTenant = func() (uuid.UUID, error) {
			id := uuid.New()
			c.mu.Lock()
			defer c.mu.Unlock()
			c.tenants[id] = true
			return id, nil
		}
		env.TenantCtx = func(id uuid.UUID) context.Context { return context.WithValue(t.Context(), scopeKey{}, id) }
	}
	return env
}

// An honest counter passes, scoped or not: the faults below fail for what
// they get wrong, not for what the suite asks of every store.
func TestRunCounterPassesAnHonestCounter(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprintf("scoped %v", scoped), func(t *testing.T) {
			metertest.RunCounter(t, func(t *testing.T) metertest.CounterEnv { return counterEnv(t, scoped, "") })
		})
	}
}

// faultyRunName is the test a child process runs the suite in.
const faultyRunName = "TestRunCounterFailsAFaultyCounter"

func TestRunCounterFailsAFaultyCounter(t *testing.T) {
	if fault := os.Getenv(faultEnv); fault != "" {
		_, scoped := scopedFaults[fault]
		metertest.RunCounter(t, func(t *testing.T) metertest.CounterEnv { return counterEnv(t, scoped, fault) })
		return
	}
	for fault, check := range faultChecks() {
		t.Run(fault, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+faultyRunName+"$", "-test.v")
			cmd.Env = append(os.Environ(), faultEnv+"="+fault)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("the suite passed a counter that %s", fault)
			}
			if failed := "--- FAIL: " + faultyRunName + "/" + check; !strings.Contains(string(out), failed) {
				t.Errorf("a counter that %s did not fail %s:\n%s", fault, check, out)
			}
		})
	}
}

// scopedFaults are the faults of a scoping store.
var scopedFaults = map[string]struct{}{faultIgnoresScope: {}, faultCountsRefused: {}}

// faultChecks is the check each fault must fail.
func faultChecks() map[string]string {
	return map[string]string{
		faultMiscounts:        "BumpCountsUpToItsOwnCeiling",
		faultMiscountsRefusal: "AZeroCeilingAllowsNothing",
		faultHidesRefusal:     "BumpCountsUpToItsOwnCeiling",
		faultIgnoresScope:     "ACallScopedToAnotherTenantIsRefused",
		faultCountsRefused:    "AReadOnlyScopeCountsNothing",
	}
}
