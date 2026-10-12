package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// subjectSpec is a meter subject's parts.
type subjectSpec struct {
	capID  uuid.UUID
	tenant uuid.UUID
	// requests and budget are the subject's ceilings; nil, a check that
	// sets none, is Unlimited.
	requests *limes.RequestCeiling
	budget   *limes.BudgetCeiling
	unit     string
	copies   []limes.CopyCeiling
}

// newSubject is the MeterSubject a verified capability with spec's parts
// would give, so a test can meter against ceilings no issuer signed.
func newSubject(spec subjectSpec) limes.MeterSubject {
	return link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: spec.capID, TenantID: spec.tenant,
		Requests: spec.requestCeiling(), Budget: spec.budgetCeiling(), UnitCode: spec.unit, Copies: spec.copies,
	}).(limes.MeterSubject)
}

func (spec subjectSpec) requestCeiling() limes.RequestCeiling {
	if spec.requests == nil {
		return limes.RequestCeiling{Unlimited: true}
	}
	return *spec.requests
}

func (spec subjectSpec) budgetCeiling() limes.BudgetCeiling {
	if spec.budget == nil {
		return limes.BudgetCeiling{Unlimited: true}
	}
	return *spec.budget
}

// budgetOf and requestsOf are ceilings of n, for a spec to set.
func budgetOf(n limes.Nanos) *limes.BudgetCeiling { return &limes.BudgetCeiling{Max: n} }
func requestsOf(n int64) *limes.RequestCeiling    { return &limes.RequestCeiling{Max: n} }

// testTenant is a tenant for a test that meters under one and cares not which.
var testTenant = uuid.MustParse("00000000-0000-4000-8000-000000000001")

// newTestStore and newTestUsage are the stores a test builds with the
// defaults; neither constructor fails on them.
func newTestStore() *Store {
	s, err := NewStore(StoreConfig{})
	if err != nil {
		panic(err)
	}
	return s
}

func newTestUsage(records *Store) *UsageStore[struct{}] {
	u, err := NewUsageStore(UsageStoreConfig[struct{}]{Records: records})
	if err != nil {
		panic(err)
	}
	return u
}

// mustUsage is NewUsageStore of cfg, failing the test on an error.
func mustUsage(t testing.TB, cfg UsageStoreConfig[struct{}]) *UsageStore[struct{}] {
	t.Helper()
	u, err := NewUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// recordedSubject records spec's capability as a root in u's record store,
// unless it is already there, and returns its subject: the Meter meters only
// a capability on record.
func recordedSubject[TX any](ctx context.Context, t testing.TB, u *UsageStore[TX], spec subjectSpec) limes.MeterSubject {
	t.Helper()
	c := limes.Capability{
		ID: spec.capID, Subject: limes.Principal{Type: limes.PrincipalAgent, TenantID: spec.tenant, Subject: "agent"},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true},
		},
		ExpiresAt: time.Now().Add(contractTTL),
	}
	err := u.records.Insert(ctx, limes.InsertRequest{Capability: c, IssuedBy: limes.Principal{Subject: "issuer"}})
	if err != nil && !errors.Is(err, limes.ErrAlreadyExists) {
		t.Fatal(err)
	}
	return newSubject(spec)
}

// biscuitCopy is the BiscuitCopy a verifier would read for a copy of capID
// with revocation id id.
func biscuitCopy(capID uuid.UUID, id []byte) limes.BiscuitCopy {
	return link.NewBiscuitCopy(capID, id).(limes.BiscuitCopy)
}
