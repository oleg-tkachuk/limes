package storetest

import (
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/fields"
)

// The round trip proves a field survives a store only when the fixture sets
// it: a field the module adds, left zero here, passes a store that drops it.
func TestFixturesSetEveryField(t *testing.T) {
	f := fixture{Env: Env{Tenant: uuid.New()}, now: time.Now()}
	unset := []string{
		// Zero beside a Max; checkRoundTrip sets each in a capability of its own.
		"Caveats.Requests.Unlimited", "Caveats.Budget.Unlimited", "Caveats.Copies.Unlimited",
	}
	if got := fields.Unset(f.newCapability("agent:s", uuid.New(), f.now.Add(ttl))); !slices.Equal(got, unset) {
		t.Errorf("newCapability leaves %q unset, want only %q", got, unset)
	}
	if got := fields.Unset(f.issuer()); len(got) > 0 {
		t.Errorf("issuer leaves %q unset", got)
	}
}

// checkRecord reads back a Record's capability, its issuer and its
// revocation, and checkSessionOnRecord its session; a field the module adds
// to Record needs a check of its own.
func TestRecordFieldsAreChecked(t *testing.T) {
	want := []string{"Capability", "IssuedBy", "Session", "Revocation"}
	if got := fields.Names[limes.Record](); !slices.Equal(got, want) {
		t.Errorf("limes.Record has fields %q; checkRecord reads back %q", got, want)
	}
}
