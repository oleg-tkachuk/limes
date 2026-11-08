package biscuit

import (
	"crypto/ed25519"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
)

const (
	// subSecond is a fraction of a second a Biscuit date cannot carry.
	subSecond = 750 * time.Millisecond
	// The zone an expiry is asked in, which a Biscuit date does not carry.
	otherZoneName   = "UTC+3"
	otherZoneOffset = 3 * time.Hour
	// expiryAhead is how far ahead the attenuation's expiry is, within the
	// fixture's TTL.
	expiryAhead = time.Minute
)

// fullAttenuation sets every field of limes.Attenuation, the expiry with a
// fraction of a second and outside UTC.
func fullAttenuation(t *testing.T) limes.Attenuation {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	jkt, err := dpop.Thumbprint(pub)
	if err != nil {
		t.Fatal(err)
	}
	return limes.Attenuation{
		Ops:              []limes.Op{limes.OpGet, limes.OpList},
		ResourcePrefixes: []string{"corpus/a/"},
		ResourceURIs:     []string{"corpus/b/doc"},
		Audience:         []string{audienceData},
		ExpiresAt:        time.Now().Add(expiryAhead).Add(subSecond).In(time.FixedZone(otherZoneName, int(otherZoneOffset.Seconds()))),
		ConfirmationJKT:  jkt,
		Requests:         &limes.RequestCeiling{Max: 10},
		Budget:           &limes.BudgetCeiling{Max: limes.Nanos(1_000)},
	}
}

// Every field of limes.Attenuation is reflected in this test's attenuation,
// so a field a later limes adds fails here until the block encodes it.
func TestFullAttenuationSetsEveryField(t *testing.T) {
	v := reflect.ValueOf(fullAttenuation(t))
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("limes.Attenuation.%s is left zero", v.Type().Field(i).Name)
		}
	}
}

// An attenuation setting every field, its expiry to a fraction of a second
// in another zone, is accepted and reads back as it was asked for.
func TestAttenuateAcceptsWhatItEncodes(t *testing.T) {
	_, _, token := biscuitFixture(t)
	a := fullAttenuation(t)
	attenuated, err := Attenuate(token, a)
	if err != nil {
		t.Fatalf("Attenuate(every field): %v", err)
	}
	got, err := encodedAttenuation(attenuated)
	if err != nil {
		t.Fatal(err)
	}
	if !sameAttenuation(a, got) {
		t.Errorf("read back %+v, want %+v", got, a)
	}
	if want := a.ExpiresAt.UTC().Truncate(time.Second); !got.ExpiresAt.Equal(want) {
		t.Errorf("expiry read back %s, want %s", got.ExpiresAt, want)
	}
	for name, a := range map[string]limes.Attenuation{
		"empty lists": {Ops: []limes.Op{}, ResourcePrefixes: []string{}, ResourceURIs: []string{}, Audience: []string{}, Requests: &limes.RequestCeiling{}},
		"nothing":     {},
	} {
		if _, err := Attenuate(token, a); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An attenuation the block would carry otherwise than it was asked for is
// refused at Attenuate, rather than handed out as a token whose restriction
// is not the one asked for: here a resource the verifier's reader refuses.
func TestAttenuateRefusesWhatItCannotReadBack(t *testing.T) {
	_, _, token := biscuitFixture(t)
	if _, err := Attenuate(token, limes.Attenuation{ResourcePrefixes: []string{""}}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("an empty resource prefix: err = %v, want ErrInvalidRequest", err)
	}
}

// sameAttenuation sees every field, and differs on any one a block did not
// carry: what a field of a later limes, which this release writes no fact
// for, reads back as.
func TestSameAttenuationSeesEveryField(t *testing.T) {
	full := fullAttenuation(t)
	if !sameAttenuation(full, full) {
		t.Fatal("an attenuation differs from itself")
	}
	v := reflect.ValueOf(full)
	for i := range v.NumField() {
		dropped := full
		reflect.ValueOf(&dropped).Elem().Field(i).SetZero()
		if sameAttenuation(full, dropped) {
			t.Errorf("an attenuation without its %s is the same", v.Type().Field(i).Name)
		}
	}
	// What a block cannot tell apart is the same.
	if !sameAttenuation(limes.Attenuation{Ops: []limes.Op{}}, limes.Attenuation{}) {
		t.Error("an empty list differs from none")
	}
	at := full.ExpiresAt
	if !sameAttenuation(limes.Attenuation{ExpiresAt: at}, limes.Attenuation{ExpiresAt: at.UTC().Truncate(time.Second)}) {
		t.Error("an expiry differs from its whole seconds in UTC")
	}
}
