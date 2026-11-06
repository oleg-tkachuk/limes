package metertest_test

import (
	"cmp"
	"context"
	"errors"
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

// The sibling races are worth the store they fail: one that locks only the
// subject's own counters, and reads every ceiling above it unlocked. It
// passes a capability racing itself, and lets siblings, cousins under one
// tenant and grandchildren spend past what they share. Each check runs in a
// child test process against that store.

// perSubjectEnv names the check a child process runs against the
// per-subject store.
const perSubjectEnv = "LIMES_METERTEST_PER_SUBJECT_CHECK"

// readToWriteGap is how long the per-subject store waits between reading
// the ceilings and charging: long enough that every racer reads before any
// writes, on any GOMAXPROCS.
const readToWriteGap = 10 * time.Millisecond

// perSubject is a memstore usage store whose Charge locks the subject alone:
// it checks the subject's, every ancestor's and the tenant's ceiling from
// their counters under that lock, then charges past every ceiling.
type perSubject struct {
	*memstore.UsageStore
	records *memstore.Store
	// locks holds a *sync.Mutex per capability charged.
	locks sync.Map
}

func (s *perSubject) Charge(ctx context.Context, req meter.ChargeRequest, onCharged func(context.Context, meter.Charged) error) (meter.ChargeReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.ChargeReceipt{}, err
	}
	lock, _ := s.locks.LoadOrStore(req.Subject.CapabilityID(), &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if err := s.room(ctx, req.Subject, req.Amount); err != nil {
		return meter.ChargeReceipt{}, err
	}
	time.Sleep(readToWriteGap)
	req.Overrun = meter.OverrunPolicyRecord
	return s.UsageStore.Charge(ctx, req, onCharged)
}

// room refuses amount when a counter on the subject's chain, or its
// tenant's, has no room for it as last read.
func (s *perSubject) room(ctx context.Context, subject limes.MeterSubject, amount limes.Nanos) error {
	id := subject.CapabilityID()
	ceiling := subject.Budget()
	for id != uuid.Nil() {
		u, err := s.GetUsage(ctx, id)
		if err != nil && !errors.Is(err, meter.ErrUsageNotFound) {
			return err
		}
		if !ceiling.Admits(u.SpentAmount + u.ReservedAmount + amount) {
			return limes.ErrBudgetExceeded
		}
		rec, err := s.records.GetRecord(ctx, id)
		if err != nil {
			return err
		}
		id = rec.Capability.ParentID
		if id != uuid.Nil() {
			parent, err := s.records.GetRecord(ctx, id)
			if err != nil {
				return err
			}
			ceiling = parent.Capability.Caveats.Budget
		}
	}
	b, err := s.GetTenantBudget(ctx, subject.TenantID())
	switch {
	case errors.Is(err, meter.ErrTenantBudgetNotFound):
		return nil
	case err != nil:
		return err
	case !b.Ceiling.Admits(b.SpentAmount + b.ReservedAmount + amount):
		return limes.ErrTenantBudgetExceeded
	}
	return nil
}

// perSubjectStore is an Env over a fresh per-subject store.
func perSubjectStore(t *testing.T) metertest.Env {
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
	expiry := time.Now().Add(counterTTL)
	return metertest.Env{
		Ctx:    t.Context(),
		Usage:  &perSubject{UsageStore: usage, records: records},
		Tenant: tenant,
		NewCapability: func(spec metertest.CapabilitySpec) (uuid.UUID, error) {
			c := limes.Capability{
				ID: uuid.New(), ParentID: spec.Parent, ExpiresAt: expiry,
				Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: cmp.Or(spec.Tenant, tenant), Subject: "agent"},
				Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: spec.Requests, Budget: spec.Budget, Copies: spec.CopyQuota()},
			}
			return c.ID, records.Insert(context.Background(), limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}})
		},
	}
}

// perSubjectRunName is the test a child process runs the suite in.
const perSubjectRunName = "TestRunFailsAPerSubjectStore"

// perSubjectChecks is whether the per-subject store must fail each check:
// the races of what siblings share, and not those of one capability.
var perSubjectChecks = map[string]bool{
	"SiblingsChargingAtOnceKeepTheirRootsCeiling":      true,
	"CapabilitiesChargingAtOnceKeepTheTenantCeiling":   true,
	"GrandchildrenChargingAtOnceKeepEveryCeilingAbove": true,
	"ConcurrentChargesKeepTheCeiling":                  false,
	"AnAncestorCeilingBoundsItsChildren":               false,
}

func TestRunFailsAPerSubjectStore(t *testing.T) {
	if os.Getenv(perSubjectEnv) != "" {
		metertest.Run(t, perSubjectStore)
		return
	}
	for check, fails := range perSubjectChecks {
		t.Run(check, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+perSubjectRunName+"$/^"+check+"$", "-test.v")
			cmd.Env = append(os.Environ(), perSubjectEnv+"="+check)
			out, err := cmd.CombinedOutput()
			failed := strings.Contains(string(out), "--- FAIL: "+perSubjectRunName+"/"+check)
			passed := strings.Contains(string(out), "--- PASS: "+perSubjectRunName+"/"+check)
			switch {
			case fails && (err == nil || !failed):
				t.Fatalf("a store that locks only the subject passed %s:\n%s", check, out)
			case !fails && (err != nil || !passed):
				t.Fatalf("a store that locks only the subject failed %s, which a capability racing itself passes:\n%s", check, out)
			}
		})
	}
}
