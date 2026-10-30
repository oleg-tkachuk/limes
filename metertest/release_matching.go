package metertest

import (
	"errors"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// heldEach is what each hold of the ReleaseMatching checks reserves.
const heldEach = one

// releaser is f's MatchingReleaser, skipping the check without one or
// without Env.Revoke.
func (f fixture) releaser(t *testing.T) meter.MatchingReleaser {
	t.Helper()
	r, ok := f.Usage.(meter.MatchingReleaser)
	if !ok || f.Revoke == nil {
		t.Skip("Env.Usage is no meter.MatchingReleaser, or Env.Revoke not set")
	}
	return r
}

// heldCapability records a capability of client under parent and reserves
// heldEach against it, and returns it and its hold.
func (f fixture) heldCapability(t *testing.T, parent uuid.UUID, client string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	id, err := f.NewCapability(CapabilitySpec{
		Parent: parent, Requests: limes.RequestCeiling{Unlimited: true}, Budget: unlimitedBudget, ClientID: client,
	})
	expect.Must(t, err == nil, "record a capability: %v", err)
	hold, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{
		Subject: newSubject(subjectSpec{capID: id, tenant: f.Tenant, unit: unit}), Amount: heldEach,
	})
	expect.Must(t, err == nil, "reserve: %v", err)
	return id, hold.ID
}

// open reports whether hold still counts.
func (f fixture) open(t *testing.T, hold uuid.UUID) bool {
	t.Helper()
	_, err := f.Usage.GetReservation(f.Ctx, hold)
	expect.Must(t, err == nil || errors.Is(err, meter.ErrReservationNotFound), "GetReservation %s: %v", hold, err)
	return err == nil
}

// The holds of a revoked capability of the client, and of one delegated
// from it, are released, and what they held returns to the capability and
// the tenant; a live capability of the client keeps its hold, and so does a
// revoked one of another client.
func checkReleaseMatching(t *testing.T, f fixture) {
	r := f.releaser(t)
	client, other := "client-"+uuid.New().String(), "client-"+uuid.New().String()
	revoked, revokedHold := f.heldCapability(t, uuid.Nil(), client)
	_, childHold := f.heldCapability(t, revoked, "")
	_, liveHold := f.heldCapability(t, uuid.Nil(), client)
	theirs, theirsHold := f.heldCapability(t, uuid.Nil(), other)
	expect.Must(t, f.Revoke(revoked) == nil && f.Revoke(theirs) == nil, "revoke")
	before, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.Must(t, err == nil, "tenant budget: %v", err)
	got, err := r.ReleaseMatching(f.Ctx, meter.ReleaseMatchingRequest{TenantID: f.Tenant, ClientID: client})
	expect.Must(t, err == nil, "ReleaseMatching: %v", err)
	expect.That(t, got.Released == 2 && !got.More, "ReleaseMatching = %+v, want the revoked capability's hold and its child's, and no more", got)
	expect.That(t, !f.open(t, revokedHold) && !f.open(t, childHold), "a hold of the revoked capability or of its child still counts")
	expect.That(t, f.open(t, liveHold), "the hold of a live capability of the client was released")
	expect.That(t, f.open(t, theirsHold), "the hold of another client's revoked capability was released")
	u, err := f.Usage.GetUsage(f.Ctx, revoked)
	expect.That(t, err == nil && u.ReservedAmount == 0, "the revoked capability after the release = %+v, %v; want nothing held", u, err)
	after, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, err == nil && before.ReservedAmount-after.ReservedAmount == 2*heldEach,
		"the tenant held %v, then %v (%v); want %v returned", before.ReservedAmount, after.ReservedAmount, err, 2*heldEach)
	again, err := r.ReleaseMatching(f.Ctx, meter.ReleaseMatchingRequest{TenantID: f.Tenant, ClientID: client})
	expect.That(t, err == nil && again.Released == 0 && !again.More, "a second call = %+v, %v; want nothing left", again, err)
}

// A batch releases at most Limit and says when more may match; the loop
// ends with every hold released.
func checkReleaseMatchingBatches(t *testing.T, f fixture) {
	r := f.releaser(t)
	const holds, batch = 3, 2
	client := "client-" + uuid.New().String()
	root, _ := f.heldCapability(t, uuid.Nil(), client)
	for range holds - 1 {
		f.heldCapability(t, root, "")
	}
	expect.Must(t, f.Revoke(root) == nil, "revoke")
	var total int64
	for calls := 0; ; calls++ {
		expect.Must(t, calls <= holds, "ReleaseMatching never reported the last batch")
		got, err := r.ReleaseMatching(f.Ctx, meter.ReleaseMatchingRequest{TenantID: f.Tenant, ClientID: client, Limit: batch})
		expect.Must(t, err == nil, "ReleaseMatching: %v", err)
		expect.That(t, got.Released <= batch, "a batch released %d, past its limit %d", got.Released, batch)
		total += got.Released
		if !got.More {
			break
		}
	}
	expect.That(t, total == holds, "the batches released %d, want %d", total, holds)
}

func checkReleaseMatchingMalformed(t *testing.T, f fixture) {
	r := f.releaser(t)
	for name, req := range map[string]meter.ReleaseMatchingRequest{
		"no tenant":        {ClientID: "client"},
		"no selector":      {TenantID: f.Tenant},
		"a negative limit": {TenantID: f.Tenant, ClientID: "client", Limit: -1},
	} {
		_, err := r.ReleaseMatching(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "%s: %v, want ErrInvalidRequest", name, err)
	}
	if !f.ScopesTenants {
		return
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	_, err = r.ReleaseMatching(f.Ctx, meter.ReleaseMatchingRequest{TenantID: other, ClientID: "client"})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "a call scoped to another tenant: %v, want ErrScopeMismatch", err)
}
