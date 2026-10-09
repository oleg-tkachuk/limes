package limes

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// UsageStore is everything a consumer implements for runtime accounting. It
// is the union of three narrower contracts so that code which only meters
// (an interceptor, a handler) can depend on Meter alone, and code which only
// administers tenant ceilings can depend on TenantBudgets alone.
type UsageStore[TX any] interface {
	Meter[TX]
	TenantBudgets
	UsageHousekeeping
}

// Meter enforces the MaxRequests and MaxBudgetAmount caveats and keeps the
// charges ledger. Separated from Store so the issuance / revocation surface
// stays focused on identity, not runtime state.
//
// # Delegation trees
//
// A capability's counters cover its whole subtree: Bump and Charge
// apply to the capability AND to every ancestor in its delegation chain, and
// each ancestor's own ceiling — read from its stored record, not from the
// caller — must admit the increment. So an orchestrator holding 25.00 that
// delegates 2.00 to each of a hundred workers can still spend 25.00 in total,
// not 200.00. Narrowing at delegation bounds each child; this bounds the sum.
// An ancestor whose record is gone (purged after expiry) no longer bounds
// anything — its children expired with it, since a child cannot outlive its
// parent.
//
// # Copies of a Biscuit
//
// A Biscuit's holder can give a copy limits of its own (CopyCeiling), which
// arrive as Copies on BumpRequest, ChargeRequest and ReserveRequest. Each is
// counted under its revocation id, apart from its siblings, and checked
// before the capability's ceilings; the capability's counters still take
// every copy's requests and spend. A charge or reservation records the copies
// it debited, so Refund, Settle, Release and ReleaseExpired return to them
// without being handed them again. A Meter that does not count copies must
// not be paired with a verifier that sets VerifierConfig.MeterCopies.
//
// All methods are concurrency-safe. A rejected call mutates nothing, so a
// retry after rejection is safe.
type Meter[TX any] interface {
	// Bump increments the request counter of the capability and of
	// each ancestor, and returns the capability's new count. When any
	// ceiling in the chain would be crossed it returns
	// ErrRequestLimitExceeded and nothing is mutated.
	Bump(ctx context.Context, req BumpRequest) (newCount int64, err error)

	// Charge adds req.Amount to the spend counters of the capability, of
	// each ancestor and of the tenant aggregate, writes one charges-ledger
	// row, and runs onCharged — ALL atomically. Checks, in order:
	//
	//   1. Each Biscuit copy's own budget (req.Copies), innermost first →
	//      ErrBudgetExceeded.
	//   2. The capability's own ceiling (req.MaxBudget, from the verified
	//      token) → ErrBudgetExceeded.
	//   3. Each ancestor's ceiling → ErrBudgetExceeded.
	//   4. The tenant aggregate ceiling → ErrTenantBudgetExceeded.
	//
	// Any rejection, or an error from onCharged, leaves every counter and
	// the ledger untouched. onCharged runs inside the charge, atomically
	// with it, and receives the consumer's transaction handle, so a side
	// effect such as an outbox write commits with the spend. Pass nil to
	// skip it. It must not call back into the Meter: an implementation may
	// hold its counters while onCharged runs, so a nested call can wait on
	// itself.
	//
	// The returned receipt carries the ledger row's ID; Refund takes it.
	//
	// With req.ExternalRef set, Charge is idempotent on (CapabilityID,
	// ExternalRef): a charge whose pair is already in the ledger moves
	// nothing, does not run onCharged, and returns that charge's receipt
	// with Replayed set. With req.Overrun = OverrunRecord, a charge that
	// would cross a ceiling is committed anyway and the receipt reports it
	// (ChargeReceipt.Overrun); see OverrunPolicy.
	Charge(ctx context.Context, req ChargeRequest, onCharged func(ctx context.Context, tx TX) error) (ChargeReceipt, error)

	// Refund returns spend from one recorded charge to every counter that
	// charge debited (capability, ancestors, tenant aggregate), and records
	// the refund against the charge. req.Amount = 0 refunds whatever
	// remains, which makes a full refund idempotent; a partial refund that
	// would take the total refunded past the charge returns
	// ErrRefundExceedsCharge and changes nothing. An unknown charge ID
	// returns ErrChargeNotFound. Returns the amount actually refunded.
	//
	// For a cost only known afterwards, prefer Reserve/Settle: a
	// reservation lapses on its own if the caller never settles it, where
	// an estimate charged up front stays charged.
	Refund(ctx context.Context, req RefundRequest) (refunded Nanos, err error)

	// Reserve holds req.Amount against the capability, each ancestor and
	// the tenant aggregate, checking every ceiling against spend plus
	// what is already held. Holds count against later charges and
	// reservations exactly as spend does, until the reservation is
	// settled, released or expires. Rejections return the Charge
	// sentinels and hold nothing.
	Reserve(ctx context.Context, req ReserveRequest) (Reservation, error)

	// Settle ends a reservation and charges the actual cost in its place,
	// atomically: the hold is released and req.Amount is charged as
	// Charge would, with onCharged run inside. The actual cost may be
	// below, equal to or above the hold; above it, the excess must fit
	// every ceiling, or Settle returns the Charge sentinel and the
	// reservation stays in place for the caller to settle lower or
	// release — unless req.Overrun is OverrunRecord, when the cost is
	// charged anyway and the receipt reports it.
	//
	// Settle is idempotent on the reservation: settling one already
	// settled moves nothing, does not run onCharged, and returns the
	// receipt of the charge that settled it, with Replayed set. An unknown,
	// released or expired reservation returns ErrReservationNotFound.
	Settle(ctx context.Context, req SettleRequest, onCharged func(ctx context.Context, tx TX) error) (ChargeReceipt, error)

	// GetCharge returns one charge of the ledger, by the ChargeID its
	// receipt carried — the id Refund takes. ErrChargeNotFound when there
	// is none.
	GetCharge(ctx context.Context, chargeID uuid.UUID) (ChargeRecord, error)

	// ChargeByRef returns the charge a capability took under externalRef
	// (ChargeRequest.ExternalRef), so a reporter can reconcile its own
	// records with the ledger. ErrChargeNotFound when there is none.
	ChargeByRef(ctx context.Context, capID uuid.UUID, externalRef string) (ChargeRecord, error)

	// GetReservation returns a reservation still holding budget — open,
	// or expired and not yet released, since until ReleaseExpired runs it
	// still counts (ExpiresAt tells which). ErrReservationNotFound once it
	// is settled or released.
	GetReservation(ctx context.Context, reservationID uuid.UUID) (Reservation, error)

	// ListReservations returns every reservation GetReservation would
	// return that was made against the capability itself — not its
	// delegated children — soonest to expire first.
	ListReservations(ctx context.Context, capID uuid.UUID) ([]Reservation, error)

	// Release ends a reservation without charging. Idempotent: releasing
	// one that is gone is a no-op.
	Release(ctx context.Context, reservationID uuid.UUID) error

	// GetUsage returns the current snapshot. Returns ErrUsageNotFound when no
	// row exists for the capability — typically means it's never been
	// used (no requests, no charges). For a capability with delegated
	// children the counters include the children's usage.
	GetUsage(ctx context.Context, capID uuid.UUID) (Usage, error)
}

// TenantBudgets administers the tenant-aggregate ceilings Charge enforces.
type TenantBudgets interface {
	// GetTenantBudget returns the tenant-aggregate snapshot. Returns
	// ErrTenantBudgetNotFound when no row exists (tenant has no cap
	// configured AND has never been charged).
	GetTenantBudget(ctx context.Context, tenantID uuid.UUID) (TenantBudget, error)

	// SetTenantBudget upserts the tenant cap. ResetSpend=true rolls
	// the accounting period and zeroes the spend — operators call
	// this on each billing close. ResetSpend=false adjusts the cap
	// mid-cycle without affecting accumulated spend. A ceiling outside
	// 0..MaxNanos returns ErrInvalidAmount and changes nothing.
	SetTenantBudget(ctx context.Context, req SetTenantBudgetRequest) (TenantBudget, error)

	// ListTenantBudgets returns tenant budget rows, with the consumer's
	// display fields where it has them. Filters:
	//   - excludeInactive: skip tenants the consumer considers inactive.
	//   - thresholdPct > 0: include only rows where
	//     spent / max * 100 >= thresholdPct (and max > 0).
	//   - unlimitedOnly: include only rows where max == 0.
	//   - limit: the page size, as PageLimit reads it.
	//
	// Rows come most at risk first: by spent / max × 100 descending —
	// unclamped, so of two tenants past their ceilings the further one
	// leads — then by tenant id. A page ends with a cursor that fetches
	// the next one, empty on the last page. Spend moves between pages, so
	// a tenant may show on two of them, or on none; a cursor is a place in
	// the order, not a snapshot.
	ListTenantBudgets(ctx context.Context, req ListTenantBudgetsRequest) ([]TenantBudgetSummary, string, error)
}

// UsageHousekeeping reclaims usage rows of capabilities that are gone.
type UsageHousekeeping interface {
	// Delete drops the row. Called when the capability is reaped.
	Delete(ctx context.Context, capID uuid.UUID) error

	// PurgeOrphans deletes usage rows whose capability record no longer
	// exists. Implementations may bound the work per call; the caller
	// loops until it returns 0.
	PurgeOrphans(ctx context.Context) (int64, error)

	// ReleaseExpired releases every reservation past its expiry and
	// returns how many it released. Run on a timer: until it runs, an
	// expired hold keeps counting against the ceilings, which errs towards
	// refusing spend, never towards allowing too much.
	ReleaseExpired(ctx context.Context) (int64, error)
}

// BumpRequest is the input to Meter.Bump.
type BumpRequest struct {
	CapabilityID uuid.UUID
	// TenantID is the capability's tenant. Used for attribution (metrics);
	// it is not an authorisation input.
	TenantID uuid.UUID
	// MaxRequests is the capability's own ceiling, from the verified
	// token. 0 = unlimited. Ancestors' ceilings come from their records.
	MaxRequests int64
	// Copies are the presented Biscuit copy's own limits, from the verified
	// token (Capability.Copies), innermost first.
	Copies []CopyCeiling
}

// ChargeRequest is the input to Meter.Charge.
type ChargeRequest struct {
	CapabilityID uuid.UUID
	// TenantID is the capability's tenant: its aggregate ceiling applies
	// and the ledger row is filed under it. Required.
	TenantID uuid.UUID
	// Amount is the cost, in UnitCode. Must be finite and ≥ 0.
	Amount Nanos
	// MaxBudget is the capability's own ceiling, from the verified token.
	// 0 = unlimited.
	MaxBudget Nanos
	// UnitCode is the currency or unit; empty means DefaultUnitCode. No
	// conversion happens: a charge in a unit other than the counter's is a
	// configuration error the caller must prevent.
	UnitCode string
	// Op and Actor are stamped on the ledger row for attribution only;
	// both may be empty and neither affects enforcement.
	Op    string
	Actor string
	// Copies are the presented Biscuit copy's own limits (Capability.Copies).
	Copies []CopyCeiling
	// ExternalRef names the cost in the consumer's own records — the id of
	// the call or the event that reported it — and makes the charge
	// idempotent on (CapabilityID, ExternalRef). Empty: every call charges.
	// At most MaxExternalRefBytes long.
	ExternalRef string
	// Overrun is what happens when Amount would cross a ceiling.
	Overrun OverrunPolicy
}

// ChargeRecord is one charge as the ledger holds it.
type ChargeRecord struct {
	ChargeID     uuid.UUID
	CapabilityID uuid.UUID
	// Amount is what the charge took; Refunded is what has been returned
	// from it since.
	Amount   Nanos
	Refunded Nanos
	UnitCode string
	// ExternalRef is the charge's ChargeRequest.ExternalRef; empty for none.
	ExternalRef string
	// Overrun reports that the charge crossed a ceiling (OverrunRecord).
	Overrun bool
}

// MaxExternalRefBytes bounds ChargeRequest.ExternalRef: long enough for any
// UUID, ULID or provider call id, short enough to index.
const MaxExternalRefBytes = 200

// OverrunPolicy is what a charge does when its amount would cross a ceiling —
// a Biscuit copy's, the capability's, an ancestor's or the tenant's.
type OverrunPolicy uint8

const (
	// OverrunReject refuses the charge with the ceiling's sentinel and
	// changes nothing. Use it for a cost not yet incurred: the refusal is
	// what stops it.
	OverrunReject OverrunPolicy = iota
	// OverrunRecord commits the charge past the ceiling and reports it in
	// ChargeReceipt.Overrun. Use it for a cost already incurred — reported
	// after the fact, out of the request path — where refusing would only
	// leave the ledger short of what was spent. The crossed ceiling then
	// refuses every later charge and reservation under OverrunReject.
	OverrunRecord
)

// ChargeReceipt is the result of a committed charge.
type ChargeReceipt struct {
	// ChargeID identifies the ledger row; pass it to Meter.Refund.
	ChargeID uuid.UUID
	// Spent is the capability's spend as this call returns (its subtree's,
	// when it has delegated children).
	Spent Nanos
	// Replayed reports that the charge was already in the ledger — the same
	// ExternalRef, or a reservation already settled — so this call moved
	// nothing and ChargeID names the earlier charge.
	Replayed bool
	// Overrun reports that the charge crossed at least one ceiling, which
	// only OverrunRecord allows.
	Overrun bool
}

// DefaultReservationTTL is how long a reservation lasts when the request
// does not say.
const DefaultReservationTTL = 5 * time.Minute

// ReserveRequest is the input to Meter.Reserve.
type ReserveRequest struct {
	CapabilityID uuid.UUID
	// TenantID is the capability's tenant. Required.
	TenantID uuid.UUID
	// Amount to hold, in UnitCode. Must be finite and ≥ 0.
	Amount Nanos
	// MaxBudget is the capability's own ceiling, from the verified token.
	MaxBudget Nanos
	UnitCode  string
	// TTL is how long the hold lasts if never settled or released.
	// 0 = DefaultReservationTTL.
	TTL time.Duration
	// Op and Actor are carried onto the charge Settle records.
	Op    string
	Actor string
	// Copies are the presented Biscuit copy's own limits (Capability.Copies).
	// The reservation keeps them, and Settle checks and charges them.
	Copies []CopyCeiling
}

// Reservation is a committed hold, as Reserve made it and as GetReservation
// and ListReservations read it back.
type Reservation struct {
	ID           uuid.UUID
	CapabilityID uuid.UUID
	TenantID     uuid.UUID
	// Amount is held in UnitCode.
	Amount   Nanos
	UnitCode string
	// Op and Actor are what Settle will stamp on the charge.
	Op    string
	Actor string
	// Copies are the Biscuit copies the hold is also on, innermost first,
	// with the budget each is held against; a hold counts no requests, so
	// their MaxRequests is zero.
	Copies    []CopyCeiling
	ExpiresAt time.Time
}

// SettleRequest is the input to Meter.Settle.
type SettleRequest struct {
	ReservationID uuid.UUID
	// Amount is the actual cost to charge. Must be finite and ≥ 0.
	Amount Nanos
	// MaxBudget is the capability's own ceiling, from the verified token.
	MaxBudget Nanos
	// Overrun is what happens when Amount would cross a ceiling.
	Overrun OverrunPolicy
}

// RefundRequest is the input to Meter.Refund.
type RefundRequest struct {
	ChargeID uuid.UUID
	// Amount to refund; 0 = everything not yet refunded from this charge.
	Amount Nanos
}

// TenantBudget is the snapshot view of the tenant aggregate cap. Amounts are
// Nanos, exact; a store keeps them exactly too (an integer or decimal column),
// so that sums do not drift.
type TenantBudget struct {
	TenantID        uuid.UUID
	MaxBudgetAmount Nanos
	SpentAmount     Nanos
	// ReservedAmount is held by the tenant's open reservations.
	ReservedAmount Nanos
	UnitCode       string
	PeriodStart    time.Time
	PeriodEnd      *time.Time
	UpdatedAt      time.Time
	// ResourceVersion is the OCC token a caller passes back to Set. Zero
	// means the row has never been written.
	ResourceVersion int64
}

// SetTenantBudgetRequest is the input for UsageStore.SetTenantBudget.
type SetTenantBudgetRequest struct {
	TenantID        uuid.UUID
	MaxBudgetAmount Nanos
	// UnitCode pins the currency. Empty = keep existing or default
	// to DefaultUnitCode on first insert.
	UnitCode string
	// PeriodEnd, if non-nil, pins a closing time for the accounting
	// window. Nil = open-ended (typical for "just set the cap, I'll
	// roll later").
	PeriodEnd *time.Time
	// ResetSpend=true zeroes the spend and rolls period_start to
	// now. false leaves the counter alone — the cap changes mid-
	// window.
	ResetSpend bool
	// ExpectedVersion is the OCC guard, checked in the same statement as the
	// write. 0 asserts the row does not exist yet and is itself a conflict if
	// it does. Without this two operators editing the same cap overwrote each
	// other and neither was told.
	ExpectedVersion int64
}

// TenantBudgetSummary is a TenantBudget with the consumer's display fields
// (empty when the consumer keeps none). Returned by ListTenantBudgets.
type TenantBudgetSummary struct {
	TenantID    uuid.UUID
	Slug        string
	DisplayName string
	Budget      TenantBudget
	// UtilisationPct = spent / max × 100, clamped to [0, 100]. 0
	// when MaxBudgetAmount == 0 (unlimited/metering-only).
	UtilisationPct float64
}

// ListTenantBudgetsRequest is the input shape for
// UsageStore.ListTenantBudgets.
type ListTenantBudgetsRequest struct {
	ThresholdPct    float64
	UnlimitedOnly   bool
	ExcludeInactive bool
	Limit           int32
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
}

// TenantBudgetCursor is a place in ListTenantBudgets' order: the last row's
// utilisation, in the decimal text its store computed it in, and tenant id.
type TenantBudgetCursor struct {
	Utilisation string
	TenantID    uuid.UUID
}

// tenantBudgetCursorSep separates the cursor's two fields; neither a decimal
// nor a uuid contains it.
const tenantBudgetCursorSep = "|"

// Encode is the cursor as ListTenantBudgets hands it out: opaque, URL-safe.
func (c TenantBudgetCursor) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.Utilisation + tenantBudgetCursorSep + c.TenantID.String()))
}

// DecodeTenantBudgetCursor reads a cursor Encode wrote. Anything else is an
// ErrInvalidRequest.
func DecodeTenantBudgetCursor(s string) (TenantBudgetCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return TenantBudgetCursor{}, invalidRequest("tenant budget cursor %q", s)
	}
	pct, id, ok := strings.Cut(string(raw), tenantBudgetCursorSep)
	if !ok || pct == "" {
		return TenantBudgetCursor{}, invalidRequest("tenant budget cursor %q", s)
	}
	if _, err := strconv.ParseFloat(pct, 64); err != nil {
		return TenantBudgetCursor{}, invalidRequest("tenant budget cursor %q", s)
	}
	tenant, err := uuid.Parse(id)
	if err != nil {
		return TenantBudgetCursor{}, invalidRequest("tenant budget cursor %q", s)
	}
	return TenantBudgetCursor{Utilisation: pct, TenantID: tenant}, nil
}

// Usage is the snapshot view of a capability's runtime counters.
type Usage struct {
	CapabilityID uuid.UUID
	RequestCount int64
	SpentAmount  Nanos
	// ReservedAmount is held by open reservations (its subtree's, for a
	// capability with delegated children).
	ReservedAmount Nanos
	UnitCode       string
}

// CopyUsage is the counters of one Biscuit copy with limits of its own (see
// Meter, "Copies of a Biscuit"): the copy and every copy attenuated from it.
type CopyUsage struct {
	RevocationID   []byte
	CapabilityID   uuid.UUID
	RequestCount   int64
	SpentAmount    Nanos
	ReservedAmount Nanos
}

// CopyUsageReader reads the counters of Biscuit copies. It is separate from
// Meter so that a Meter which does not count copies implements nothing more.
type CopyUsageReader interface {
	// CopyUsage returns the counters of each copy named that has been used,
	// in no particular order; a copy never used has no counters and is
	// absent from the result.
	CopyUsage(ctx context.Context, revocationIDs [][]byte) ([]CopyUsage, error)
}

// Usage-related sentinels. ErrBudgetExceeded already lives in types.go
// (declared before the runtime enforcement landed); reused here.
var (
	// ErrInvalidAmount — a charge or refund amount that is negative, NaN
	// or infinite. Refunds are explicit; a negative charge is not one.
	ErrInvalidAmount = errors.New("limes: invalid amount")

	// ErrChargeNotFound — Refund named a charge with no ledger row.
	ErrChargeNotFound = errors.New("limes: charge not found")

	// ErrRefundExceedsCharge — Refund would return more than the charge
	// took, counting earlier refunds of the same charge.
	ErrRefundExceedsCharge = errors.New("limes: refund exceeds charge")

	// ErrReservationNotFound — Settle named a reservation that does not
	// exist, was released, or has expired. One already settled is not an
	// error: Settle returns its charge again (ChargeReceipt.Replayed).
	ErrReservationNotFound = errors.New("limes: reservation not found")

	// ErrRequestLimitExceeded — capability used MaxRequests times,
	// next attempt was blocked.
	ErrRequestLimitExceeded = errors.New("limes: request limit exceeded")

	// ErrUsageNotFound — no usage row exists for this capability.
	// Get-only; bump/charge always inserts on first hit.
	ErrUsageNotFound = errors.New("limes: usage row not found")

	// ErrTenantBudgetExceeded — the tenant aggregate cap has been
	// hit. Distinct from ErrBudgetExceeded so admin tooling can
	// render "this tenant is out of budget" vs. "this capability is
	// out of budget". Operators handle differently — tenant lift
	// is an admin RPC; capability lift requires re-issuing the
	// caveat.
	ErrTenantBudgetExceeded = errors.New("limes: tenant aggregate budget exceeded")

	// ErrTenantBudgetNotFound — Get-only; the tenant has no cap
	// configured AND has never been charged. Distinct from "cap = 0"
	// (which means "configured but unlimited").
	ErrTenantBudgetNotFound = errors.New("limes: tenant budget row not found")

	// ErrTenantBudgetVersionMismatch — SetTenantBudget's OCC guard refused the
	// write: the stored resource_version is not the one the caller read, so
	// someone else changed the cap in between. The caller re-reads and decides,
	// rather than silently overwriting a change it never saw.
	ErrTenantBudgetVersionMismatch = errors.New("limes: tenant budget resource_version mismatch")
)

// Validate reports whether p is an OverrunPolicy this package defines.
// Implementations call it before touching state, so an unknown policy is never
// read as either one.
func (p OverrunPolicy) Validate() error {
	if p > OverrunRecord {
		return fmt.Errorf("%w: unknown overrun policy %d", ErrInvalidRequest, p)
	}
	return nil
}

// ValidateExternalRef reports whether ref is usable as
// ChargeRequest.ExternalRef: empty, or printable text within
// MaxExternalRefBytes.
func ValidateExternalRef(ref string) error {
	if len(ref) > MaxExternalRefBytes {
		return fmt.Errorf("%w: external ref is %d bytes, at most %d", ErrInvalidRequest, len(ref), MaxExternalRefBytes)
	}
	for _, r := range ref {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("%w: external ref %q contains a non-printable character", ErrInvalidRequest, ref)
		}
	}
	return nil
}

// ValidateAmount reports whether a charge or refund amount is usable: not
// negative and at most MaxNanos. Implementations call it before touching
// state.
func ValidateAmount(amount Nanos) error {
	if amount < 0 || amount > MaxNanos {
		return fmt.Errorf("%w: %s is outside 0..%s", ErrInvalidAmount, amount, MaxNanos)
	}
	return nil
}
