package memstore_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/memstore"
	"github.com/oleg-tkachuk/limes/meter"
)

// exampleStart is the time every example's clock starts at.
var exampleStart = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)

// exampleTTL is how long the capabilities the examples record live.
const exampleTTL = time.Hour

// exampleEnv is a record store and a usage store over it, on a clock the
// example moves.
type exampleEnv struct {
	now     time.Time
	records *memstore.Store
	usage   *memstore.UsageStore
	tenant  uuid.UUID
}

func newExampleEnv() *exampleEnv {
	e := &exampleEnv{now: exampleStart, tenant: uuid.New()}
	clock := func() time.Time { return e.now }
	records, err := memstore.NewStore(memstore.StoreConfig{Now: clock})
	if err != nil {
		log.Fatal(err)
	}
	usage, err := memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records, Now: clock})
	if err != nil {
		log.Fatal(err)
	}
	e.records, e.usage = records, usage
	return e
}

// record writes a capability for subject — under parent, when it is not the
// zero id — with a budget of 10 USD and 100 requests.
func (e *exampleEnv) record(subject string, parent uuid.UUID) limes.Capability {
	c := limes.Capability{
		ID: uuid.Must(uuid.NewV7()), IssuedAt: e.now, ExpiresAt: e.now.Add(exampleTTL), ParentID: parent,
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: e.tenant, Subject: subject},
		Caveats: limes.Caveats{
			Ops:      []limes.Op{limes.OpGet},
			Requests: limes.RequestCeiling{Max: 100},
			Budget:   limes.BudgetCeiling{Max: limes.MustParseAmount("10"), UnitCode: "USD"},
		},
	}
	if err := e.records.Insert(context.Background(), limes.InsertRequest{
		Capability: c, IssuedBy: limes.Principal{Subject: "operator@example.com"},
	}); err != nil {
		log.Fatal(err)
	}
	return c
}

// subject is what the meter counts c against, read back from its record.
func (e *exampleEnv) subject(c limes.Capability) limes.MeterSubject {
	s, err := limes.StoredMeterSubject(context.Background(), e.records, c.ID)
	if err != nil {
		log.Fatal(err)
	}
	return s
}

func ExampleNewStore() {
	ctx := context.Background()
	e := newExampleEnv()
	parent := e.record("orchestrator", uuid.Nil)
	child := e.record("worker", parent.ID)

	record, err := e.records.GetRecord(ctx, child.ID)
	fmt.Println(err, record.Capability.ParentID == parent.ID, record.IssuedBy.Subject, record.Revocation)

	// An id already on record changes nothing.
	err = e.records.Insert(ctx, limes.InsertRequest{Capability: child, IssuedBy: record.IssuedBy})
	fmt.Println(errors.Is(err, limes.ErrAlreadyExists))
	// Output:
	// <nil> true operator@example.com <nil>
	// true
}

func ExampleStore_Revoke() {
	ctx := context.Background()
	e := newExampleEnv()
	parent := e.record("orchestrator", uuid.Nil)
	child := e.record("worker", parent.ID)

	result, err := e.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: parent.ID, Note: "agent looping", Actor: "operator@example.com"})
	fmt.Println(err, result.AlreadyRevoked, result.Revocation.Note)

	// The child stops too: IsRevoked answers for the whole chain.
	revoked, _ := e.records.IsRevoked(ctx, child.ID)
	record, _ := e.records.GetRecord(ctx, child.ID)
	fmt.Println(revoked, record.Revocation == nil)

	// CascadeChildren also writes the child's own entry, for the audit trail.
	// The parent was revoked already: its first entry stands.
	result, err = e.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: parent.ID, Note: "again", CascadeChildren: true})
	record, _ = e.records.GetRecord(ctx, child.ID)
	fmt.Println(err, record.Revocation.Cascaded, result.AlreadyRevoked, result.Revocation.Note)

	_, err = e.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: uuid.New()})
	fmt.Println(errors.Is(err, limes.ErrNotFound))
	// Output:
	// <nil> false agent looping
	// true true
	// <nil> true true agent looping
	// true
}

func ExampleStore_ListByPrincipal() {
	ctx := context.Background()
	e := newExampleEnv()
	first := e.record("researcher", uuid.Nil)
	e.record("researcher", uuid.Nil)
	e.record("someone-else", uuid.Nil)

	req := limes.ListByPrincipalRequest{
		TenantID: e.tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: "researcher", Limit: 1,
	}
	page, err := e.records.ListByPrincipal(ctx, req)
	fmt.Println(err, len(page.Items), page.Items[0].ID == first.ID, page.Next != "")

	req.Cursor = page.Next
	page, err = e.records.ListByPrincipal(ctx, req)
	fmt.Println(err, len(page.Items), page.Next)
	// Output:
	// <nil> 1 true true
	// <nil> 1
}

func ExampleStore_Purge() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)

	// Expired, but not yet for MinPurgeRetention: kept.
	e.now = c.ExpiresAt.Add(time.Minute)
	result, err := e.records.Purge(ctx, limes.PurgeRequest{})
	fmt.Println(result.Purged, err)

	e.now = c.ExpiresAt.Add(limes.MinPurgeRetention + time.Second)
	result, err = e.records.Purge(ctx, limes.PurgeRequest{})
	fmt.Println(result.Purged, err)

	// A retention shorter than MinPurgeRetention would let a revoked token
	// verify again, so it is refused.
	_, err = e.records.Purge(ctx, limes.PurgeRequest{ExpiredFor: time.Second})
	fmt.Println(errors.Is(err, limes.ErrInvalidRequest))
	// Output:
	// 0 <nil>
	// 1 <nil>
	// true
}

func ExampleStore_EraseTenantRecords() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)

	// The token may still verify, so the record stays until Purge takes it.
	result, err := e.records.EraseTenantRecords(ctx, limes.EraseTenantRequest{TenantID: e.tenant, Actor: "operator@example.com"})
	fmt.Println(err, result.Revoked, result.Removed, result.Kept)
	revoked, _ := e.records.IsRevoked(ctx, c.ID)
	fmt.Println(revoked)

	// Once no verifier would accept the token, the record goes.
	e.now = c.ExpiresAt.Add(limes.MinPurgeRetention + time.Second)
	result, err = e.records.EraseTenantRecords(ctx, limes.EraseTenantRequest{TenantID: e.tenant})
	fmt.Println(err, result.Revoked, result.Removed, result.Kept)
	// Output:
	// <nil> 1 0 1
	// true
	// <nil> 0 1 0
}

func ExampleNewUsageStore() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)

	// Bump counts a request against the ceiling; it moves no amount.
	receipt, err := e.usage.Bump(ctx, meter.BumpRequest{Subject: e.subject(c)})
	fmt.Println(receipt.RequestCount, err)

	usage, err := e.usage.GetUsage(ctx, c.ID)
	fmt.Println(usage.RequestCount, usage.SpentAmount, usage.UnitCode, err)
	// Output:
	// 1 <nil>
	// 1 0 USD <nil>
}

func ExampleUsageStore_Charge() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)
	req := meter.ChargeRequest{
		Subject: e.subject(c), Amount: limes.MustParseAmount("0.40"), Op: "index:search",
		Actor: "researcher", ExternalRef: "request-1",
	}

	// onCharged runs inside the charge: an error from it leaves every
	// counter and the ledger as they were.
	_, err := e.usage.Charge(ctx, req, func(context.Context, meter.Charged) error {
		return errors.New("write of the caller's own failed")
	})
	usage, _ := e.usage.GetUsage(ctx, c.ID)
	fmt.Println(err != nil, usage.SpentAmount)

	receipt, err := e.usage.Charge(ctx, req, nil)
	fmt.Println(err, receipt.SpentAmount, receipt.Replayed)

	// The same external reference again is a replay, charged once.
	again, err := e.usage.Charge(ctx, req, nil)
	fmt.Println(err, again.Replayed, again.ChargeID == receipt.ChargeID)

	_, err = e.usage.Charge(ctx, meter.ChargeRequest{Subject: e.subject(c), Amount: limes.MustParseAmount("100")}, nil)
	fmt.Println(errors.Is(err, limes.ErrBudgetExceeded))
	// Output:
	// true 0
	// <nil> 0.4 false
	// <nil> true true
	// true
}

func ExampleUsageStore_Charge_overrun() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)

	// A cost already incurred is recorded past the ceiling, not lost.
	receipt, err := e.usage.Charge(ctx, meter.ChargeRequest{
		Subject: e.subject(c), Amount: limes.MustParseAmount("12"), Overrun: meter.OverrunPolicyRecord,
	}, nil)
	fmt.Println(err, receipt.SpentAmount, receipt.Overrun)

	// Every later charge under the default policy is then refused.
	_, err = e.usage.Charge(ctx, meter.ChargeRequest{Subject: e.subject(c), Amount: 1}, nil)
	fmt.Println(errors.Is(err, limes.ErrBudgetExceeded))
	// Output:
	// <nil> 12 true
	// true
}

func ExampleUsageStore_ChargeByRef() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)
	receipt, err := e.usage.Charge(ctx, meter.ChargeRequest{
		Subject: e.subject(c), Amount: limes.MustParseAmount("0.25"), Op: "model:embed", ExternalRef: "invoice-7",
	}, nil)
	if err != nil {
		log.Fatal(err)
	}

	byRef, err := e.usage.ChargeByRef(ctx, meter.ChargeByRefRequest{CapabilityID: c.ID, ExternalRef: "invoice-7"})
	fmt.Println(err, byRef.ID == receipt.ChargeID, byRef.Amount, byRef.Op)

	byID, err := e.usage.GetCharge(ctx, receipt.ChargeID)
	fmt.Println(err, byID.ExternalRef, byID.At.Equal(exampleStart))
	// Output:
	// <nil> true 0.25 model:embed
	// <nil> invoice-7 true
}

func ExampleUsageStore_Refund() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)
	receipt, err := e.usage.Charge(ctx, meter.ChargeRequest{Subject: e.subject(c), Amount: limes.MustParseAmount("3")}, nil)
	if err != nil {
		log.Fatal(err)
	}

	part, err := e.usage.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("1")})
	fmt.Println(err, part.Amount)
	rest, err := e.usage.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Remaining: true})
	fmt.Println(err, rest.Amount)

	_, err = e.usage.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Amount: 1})
	fmt.Println(errors.Is(err, meter.ErrRefundExceedsCharge))
	// Output:
	// <nil> 1
	// <nil> 2
	// true
}

// Reserve holds an estimate while the cost is unknown; Settle charges the
// actual cost in its place, and Release drops a hold unused.
func ExampleUsageStore_Reserve() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)
	subject := e.subject(c)

	hold, err := e.usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: limes.MustParseAmount("2"), Op: "model:generate"})
	fmt.Println(err, hold.Amount, hold.ExpiresAt.Sub(exampleStart))

	page, _ := e.usage.ListReservations(ctx, meter.ListReservationsRequest{CapabilityID: c.ID})
	usage, _ := e.usage.GetUsage(ctx, c.ID)
	fmt.Println(len(page.Items), usage.ReservedAmount)

	receipt, err := e.usage.Settle(ctx, meter.SettleRequest{Subject: subject, ReservationID: hold.ID, Amount: limes.MustParseAmount("1.20")}, nil)
	usage, _ = e.usage.GetUsage(ctx, c.ID)
	fmt.Println(err, receipt.SpentAmount, usage.ReservedAmount)

	unused, _ := e.usage.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: limes.MustParseAmount("1")})
	fmt.Println(e.usage.Release(ctx, meter.ReleaseRequest{Subject: subject, ReservationID: unused.ID}))
	_, err = e.usage.GetReservation(ctx, unused.ID)
	fmt.Println(errors.Is(err, meter.ErrReservationNotFound))
	// Output:
	// <nil> 2 5m0s
	// 1 2
	// <nil> 1.2 0
	// <nil>
	// true
}

func ExampleUsageStore_ReleaseExpired() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)
	if _, err := e.usage.Reserve(ctx, meter.ReserveRequest{Subject: e.subject(c), Amount: 1, TTL: time.Minute}); err != nil {
		log.Fatal(err)
	}

	// A housekeeping worker frees the holds whose holders never came back.
	e.now = e.now.Add(2 * time.Minute)
	result, err := e.usage.ReleaseExpired(ctx, meter.ReleaseExpiredRequest{})
	usage, _ := e.usage.GetUsage(ctx, c.ID)
	fmt.Println(result.Released, err, usage.ReservedAmount)
	// Output:
	// 1 <nil> 0
}

func ExampleUsageStore_SetTenantBudget() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)

	budget, err := e.usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
		TenantID: e.tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("5"), UnitCode: "USD"},
	})
	fmt.Println(err, budget.Ceiling, budget.ResourceVersion)

	if _, err := e.usage.Charge(ctx, meter.ChargeRequest{Subject: e.subject(c), Amount: limes.MustParseAmount("4")}, nil); err != nil {
		log.Fatal(err)
	}
	// The capability allows 10, the tenant 5: the tenant's ceiling refuses.
	_, err = e.usage.Charge(ctx, meter.ChargeRequest{Subject: e.subject(c), Amount: limes.MustParseAmount("2")}, nil)
	fmt.Println(errors.Is(err, limes.ErrTenantBudgetExceeded))

	budget, _ = e.usage.GetTenantBudget(ctx, e.tenant)
	fmt.Println(budget.Utilization())

	// An update names the version it read: a concurrent one is refused.
	_, err = e.usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
		TenantID: e.tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("8"), UnitCode: "USD"},
		ExpectedVersion: budget.ResourceVersion + 1,
	})
	fmt.Println(errors.Is(err, meter.ErrTenantBudgetVersionMismatch))
	// Output:
	// <nil> 5 USD 1
	// true
	// 80 true
	// true
}

func ExampleUsageStore_ListTenantBudgets() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)
	if _, err := e.usage.SetTenantBudget(ctx, meter.SetTenantBudgetRequest{
		TenantID: e.tenant, Ceiling: limes.BudgetCeiling{Max: limes.MustParseAmount("10"), UnitCode: "USD"},
	}); err != nil {
		log.Fatal(err)
	}
	if _, err := e.usage.Charge(ctx, meter.ChargeRequest{Subject: e.subject(c), Amount: limes.MustParseAmount("9")}, nil); err != nil {
		log.Fatal(err)
	}

	// The tenants at or past 80% of their ceiling.
	const alertBasisPoints = 8_000
	page, err := e.usage.ListTenantBudgets(ctx, meter.ListTenantBudgetsRequest{
		Filter: meter.TenantBudgetFilterAtOrPast, ThresholdBasisPoints: alertBasisPoints,
	})
	fmt.Println(err, len(page.Items), page.Items[0].SpentAmount)
	// Output:
	// <nil> 1 9
}

func ExampleUsageStore_CopyUsage() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)

	// The counters of a capability's Biscuit copies, by revocation id
	// (limes.Verifier.BiscuitCopy); none were counted for this one.
	result, err := e.usage.CopyUsage(ctx, meter.CopyUsageRequest{CapabilityID: c.ID, RevocationIDs: [][]byte{{1, 2, 3}}})
	fmt.Println(err, len(result.Copies))
	// Output:
	// <nil> 0
}

func ExampleUsageStore_DeleteUsage() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)
	if _, err := e.usage.Bump(ctx, meter.BumpRequest{Subject: e.subject(c)}); err != nil {
		log.Fatal(err)
	}

	fmt.Println(e.usage.DeleteUsage(ctx, meter.DeleteUsageRequest{CapabilityID: c.ID}))
	_, err := e.usage.GetUsage(ctx, c.ID)
	fmt.Println(errors.Is(err, meter.ErrUsageNotFound))
	// Output:
	// <nil>
	// true
}

func ExampleUsageStore_PurgeOrphans() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)
	if _, err := e.usage.Bump(ctx, meter.BumpRequest{Subject: e.subject(c)}); err != nil {
		log.Fatal(err)
	}

	// Once Purge takes the record, PurgeOrphans takes the usage it leaves.
	e.now = c.ExpiresAt.Add(limes.MinPurgeRetention + time.Second)
	if _, err := e.records.Purge(ctx, limes.PurgeRequest{}); err != nil {
		log.Fatal(err)
	}
	result, err := e.usage.PurgeOrphans(ctx, meter.PurgeOrphansRequest{})
	fmt.Println(result.Purged, err)
	// Output:
	// 1 <nil>
}

func ExampleUsageStore_EraseTenantUsage() {
	ctx := context.Background()
	e := newExampleEnv()
	c := e.record("researcher", uuid.Nil)

	fmt.Println(e.usage.EraseTenantUsage(ctx, meter.EraseTenantUsageRequest{TenantID: e.tenant}))
	// Nothing of the tenant is counted, or issued, again.
	_, err := e.usage.Bump(ctx, meter.BumpRequest{Subject: e.subject(c)})
	fmt.Println(errors.Is(err, limes.ErrTenantErased))
	err = e.records.Insert(ctx, limes.InsertRequest{Capability: limes.Capability{
		ID: uuid.New(), IssuedAt: e.now, ExpiresAt: e.now.Add(exampleTTL),
		Subject: limes.Principal{Type: limes.PrincipalTypeService, TenantID: e.tenant, Subject: "svc"},
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}},
	}})
	fmt.Println(errors.Is(err, limes.ErrTenantErased))
	// Output:
	// <nil>
	// true
	// true
}

func ExampleNewCensus() {
	ctx := context.Background()
	e := newExampleEnv()
	census := memstore.NewCensus(e.records)
	live := e.record("researcher", uuid.Nil)
	revoked := e.record("worker", uuid.Nil)
	if _, err := e.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: revoked.ID}); err != nil {
		log.Fatal(err)
	}

	counts, err := census.CountRecords(ctx, limes.CountRecordsRequest{TenantID: e.tenant, ExpiringWithin: exampleTTL})
	n := counts.Items[0]
	fmt.Println(err, n.Total, n.Active, n.Revoked, n.ExpiringSoon)

	page, err := census.ListRecords(ctx, limes.ListRecordsRequest{
		TenantID: e.tenant, Dispositions: []limes.RecordDisposition{limes.RecordDispositionRevoked},
	})
	fmt.Println(err, len(page.Items), page.Items[0].Record.Capability.ID == revoked.ID)

	listing, err := census.GetRecordListing(ctx, live.ID)
	fmt.Println(err, listing.Disposition)
	// Output:
	// <nil> 2 1 1 1
	// <nil> 1 true
	// <nil> active
}

func ExampleNewLedger() {
	ctx := context.Background()
	e := newExampleEnv()
	ledger := memstore.NewLedger(e.usage)
	c := e.record("researcher", uuid.Nil)
	for _, charge := range []struct {
		op     limes.Op
		amount string
	}{{"index:search", "0.10"}, {"index:search", "0.15"}, {"model:embed", "1"}} {
		if _, err := e.usage.Charge(ctx, meter.ChargeRequest{
			Subject: e.subject(c), Op: charge.op, Amount: limes.MustParseAmount(charge.amount),
		}, nil); err != nil {
			log.Fatal(err)
		}
	}

	page, err := ledger.SumCharges(ctx, meter.SumChargesRequest{
		TenantID: e.tenant, From: exampleStart, To: exampleStart.Add(time.Hour),
		GroupBy: []meter.ChargeDimension{meter.ChargeDimensionOp},
	})
	if err != nil {
		log.Fatal(err)
	}
	// The largest charged come first.
	for _, sum := range page.Items {
		fmt.Println(sum.Op, sum.Charged, sum.Charges, sum.UnitCode)
	}
	// Output:
	// model:embed 1 1 USD
	// index:search 0.25 2 USD
}

// A charge stays in the ledger, answering its ExternalRef and its refunds,
// until it is older than the retention; a worker then purges it, a batch at
// a time.
func ExampleLedger_PurgeCharges() {
	ctx := context.Background()
	e := newExampleEnv()
	ledger := memstore.NewLedger(e.usage)
	c := e.record("researcher", uuid.Nil)
	if _, err := e.usage.Charge(ctx, meter.ChargeRequest{
		Subject: e.subject(c), Op: "index:search", Amount: limes.MustParseAmount("0.10"),
	}, nil); err != nil {
		log.Fatal(err)
	}
	purge := func() int64 {
		r, err := ledger.PurgeCharges(ctx, meter.PurgeChargesRequest{})
		if err != nil {
			log.Fatal(err)
		}
		return r.Purged
	}
	fmt.Println(purge())
	e.now = e.now.Add(meter.MinChargeRetention + time.Second)
	fmt.Println(purge())
	// Output:
	// 0
	// 1
}
