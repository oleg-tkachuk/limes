package biscuit

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// Blocks the reference implementation appended to the shared seed
// (docs/biscuit-profile.md), with biscuit-cli 0.6.0 over biscuit-rust:
//
//	biscuit attenuate testdata/biscuit/seed.biscuit \
//	    --block-file testdata/biscuit/foreign.datalog > testdata/biscuit/foreign.biscuit
//	biscuit attenuate testdata/biscuit/seed.biscuit \
//	    --block 'check if time($t), $t < 2026-01-01T00:30:00Z;' > testdata/biscuit/foreign-check.biscuit
//
// biscuit-rust pads its base64url, so the first also holds limes to reading
// the padded spelling.
const (
	biscuitForeignFile      = "foreign.biscuit"
	biscuitForeignCheckFile = "foreign-check.biscuit"
	// foreignCopyRequests is the request limit foreign.datalog gives the copy.
	foreignCopyRequests = 3
)

// foreignAttenuation is foreign.datalog, for Attenuate.
var foreignAttenuation = limes.Attenuation{
	Ops:              []limes.Op{limes.OpGet},
	ResourcePrefixes: []string{"corpus/public/"},
	ExpiresAt:        goldenClock().Add(30 * time.Minute),
	Requests:         &limes.RequestCeiling{Max: foreignCopyRequests},
}

// A block another implementation appended, holding facts of the profile,
// narrows the token exactly as the same block appended by Attenuate does.
func TestAForeignBlockIsReadAsLimesWrote(t *testing.T) {
	v := goldenBiscuitVerifier(t)
	ctx := context.Background()
	foreign, err := v.Verify(ctx, limes.VerifyRequest{Token: readBiscuitFixture(t, biscuitForeignFile), Audience: audienceData})
	if err != nil {
		t.Fatalf("a block biscuit-cli appended: %v", err)
	}
	own, err := Attenuate(readBiscuitFixture(t, biscuitSeedFile), foreignAttenuation)
	if err != nil {
		t.Fatal(err)
	}
	ours, err := v.Verify(ctx, limes.VerifyRequest{Token: own, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	got, want := foreign.Capability(), ours.Capability()
	if !slices.Equal(got.Caveats.Ops, want.Caveats.Ops) || !slices.Equal(got.Caveats.ResourcePrefixes, want.Caveats.ResourcePrefixes) ||
		!got.ExpiresAt.Equal(want.ExpiresAt) || got.ID != want.ID {
		t.Errorf("foreign block reads as %+v, want %+v", got, want)
	}
	copies := foreign.Copies()
	if len(copies) != 1 || copies[0].Requests == nil || copies[0].Requests.Max != foreignCopyRequests || copies[0].Budget != nil {
		t.Errorf("foreign block's copy limits = %+v, want one copy of %d requests", copies, foreignCopyRequests)
	}
}

// A check is Biscuit's language, which a limes verifier does not run: a
// block holding one is refused, whoever appended it.
func TestAForeignCheckIsRefused(t *testing.T) {
	_, err := goldenBiscuitVerifier(t).Verify(context.Background(), limes.VerifyRequest{Token: readBiscuitFixture(t, biscuitForeignCheckFile), Audience: audienceData})
	if !errors.Is(err, limes.ErrBiscuitAttenuation) {
		t.Errorf("a block with a check: err = %v, want limes.ErrBiscuitAttenuation", err)
	}
}

// A Biscuit reads the same padded or not; padding is refused unless it is
// exactly what the length needs.
func TestBiscuitPadding(t *testing.T) {
	// Lengths whose encodings need one and two padding characters.
	onePad, twoPads := []byte("ab"), []byte("a")
	cases := []struct {
		name  string
		token string
		ok    bool
	}{
		{"unpadded", base64.RawURLEncoding.EncodeToString(onePad), true},
		{"one pad", base64.URLEncoding.EncodeToString(onePad), true},
		{"two pads", base64.URLEncoding.EncodeToString(twoPads), true},
		{"pad on a full quantum", base64.RawURLEncoding.EncodeToString([]byte("abc")) + base64Pad, false},
		{"one pad short", base64.RawURLEncoding.EncodeToString(twoPads) + base64Pad, false},
		{"one pad too many", base64.URLEncoding.EncodeToString(onePad) + base64Pad, false},
		{"only padding", strings.Repeat(base64Pad, base64Quantum), false},
		// "YWI=" is "ab"; the same quantum with spare bits set names no
		// bytes of its own, and a padded spelling holds no line break either.
		{"padded, spare bits set", "YWJ" + base64Pad, false},
		{"padded, a line break", "YW\nI" + base64Pad, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := unB64Biscuit(c.token); (err == nil) != c.ok {
				t.Errorf("unB64Biscuit(%q) err = %v, want ok %v", c.token, err, c.ok)
			}
		})
	}
}
