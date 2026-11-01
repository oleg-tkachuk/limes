package limes

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// The parsers below read bytes an attacker chooses. Each must refuse what it
// cannot read — never panic — and what it accepts must hold the invariants
// the code after it relies on.

// FuzzParseClaims holds the claims reader to its strictness: whatever it
// accepts names a capability within the budget's bounds, and is what the
// signer writes for it: written again by claimsOf, it reads back as the
// same grant, so a token means one thing whichever side reads it.
func FuzzParseClaims(f *testing.F) {
	f.Add([]byte(`{"iss":"i","sub":"s","aud":["data"],"iat":1,"exp":2,"jti":"0190a5c3-0000-7000-8000-000000000000","limes_principal":{"tenant_id":"11111111-1111-1111-1111-111111111111","subject":"s"},"limes_caveats":{"ops":["get"],"max_budget":"1.5"},"client_id":"c","limes_v":7}`))
	f.Add([]byte(`{"iss":"a","iss":"b"}`))
	f.Add([]byte(`{"limes_caveats":{"Ops":["get"]}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		c, err := parseClaims(jose.B64(raw))
		if err != nil {
			return
		}
		if c.Caveats.Budget.Max < 0 || c.Caveats.Budget.Max > MaxNanos {
			t.Fatalf("accepted a budget of %d", int64(c.Caveats.Budget.Max))
		}
		again, err := json.Marshal(claimsOf(*c))
		if err != nil {
			t.Fatalf("claims of %+v do not encode: %v", c.Capability, err)
		}
		back, err := parseClaims(jose.B64(again))
		if err != nil {
			t.Fatalf("%s, written again as %s, is refused: %v", raw, again, err)
		}
		// Compared as JSON writes them, where a nil list and an empty one
		// are one value, as they are to every check of a capability.
		was, err1 := json.Marshal(c.Capability)
		now, err2 := json.Marshal(back.Capability)
		if err := errors.Join(err1, err2); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(was, now) || back.biscuitRoot != c.biscuitRoot {
			t.Fatalf("%s, written again as %s, reads back as\n%s\nnot\n%s", raw, again, now, was)
		}
		if twice, err := json.Marshal(claimsOf(*back)); err != nil || !bytes.Equal(twice, again) {
			t.Fatalf("%s written a second time as %s, not %s: %v", raw, twice, again, err)
		}
	})
}

// FuzzParseAuthorization holds the Authorization reader to what it promises:
// a scheme this module reads and a token68 credential, which read back under
// that scheme give the same answer.
func FuzzParseAuthorization(f *testing.F) {
	for _, s := range []string{"Bearer abc.def-_~+/==", "dpop  x", "Basic dXNlcg==", "Bearer a b", "Bearer ="} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, header string) {
		scheme, credential, err := ParseAuthorization(header)
		if err != nil {
			return
		}
		if scheme != AuthSchemeBearer && scheme != AuthSchemeDPoP {
			t.Fatalf("%q read as scheme %q", header, scheme)
		}
		if !isToken68(credential) {
			t.Fatalf("%q read as credential %q, not a token68", header, credential)
		}
		again, cred, err := ParseAuthorization(string(scheme) + " " + credential)
		if err != nil || again != scheme || cred != credential {
			t.Fatalf("%q reads back as %q %q, %v", header, again, cred, err)
		}
	})
}

// FuzzApplyAttenuation folds a fuzzer-chosen block into a capability, as
// whatever form decoded it hands the block to the core: what it accepts
// must narrow the capability, and every copy ceiling it adds must fit under
// the ones already in force.
func FuzzApplyAttenuation(f *testing.F) {
	f.Add(string(OpGet), "", "", int64(-1), int64(-1), int64(0), false)
	f.Add("", "corpus/public/", "", int64(-1), int64(-1), int64(0), false)
	f.Add("", "", "", int64(5), int64(-1), int64(0), false)
	f.Add("", "", "", int64(-1), int64(1_000), int64(0), true)
	f.Add("", "", audienceData, int64(-1), int64(-1), int64(1_800_000_000), false)
	start := time.Unix(1_800_000_000, 0).UTC()
	parent := validCapability()
	parent.IssuedAt, parent.NotBefore, parent.ExpiresAt = start, start, start.Add(time.Hour)
	parent.Caveats.ResourcePrefixes = []string{"corpus/"}
	parent.Caveats.Requests = RequestCeiling{Max: 10}
	parent.Caveats.Budget = BudgetCeiling{Max: NanosPerUnit}
	f.Fuzz(func(t *testing.T, op, prefix, plane string, requests, budget, expires int64, unlimited bool) {
		var a Attenuation
		if op != "" {
			a.Ops = []Op{Op(op)}
		}
		if prefix != "" {
			a.ResourcePrefixes = []string{prefix}
		}
		if plane != "" {
			a.Audience = []string{plane}
		}
		if requests >= 0 {
			a.Requests = &RequestCeiling{Max: requests, Unlimited: unlimited}
		}
		if budget >= 0 {
			a.Budget = &BudgetCeiling{Max: Nanos(budget), Unlimited: unlimited}
		}
		if expires != 0 {
			a.ExpiresAt = time.Unix(expires, 0).UTC()
		}
		next, err := applyAttenuation(grant{Capability: parent}, a, []byte("block"))
		if err != nil {
			return
		}
		if err := Narrows(parent, next.Capability); err != nil {
			t.Fatalf("accepted %+v, which widens: %v", a, err)
		}
		for _, c := range next.copies {
			if c.Requests != nil && (c.Requests.Unlimited || !c.Requests.Within(parent.Caveats.Requests)) ||
				c.Budget != nil && (c.Budget.Unlimited || !c.Budget.Within(parent.Caveats.Budget)) {
				t.Fatalf("accepted a copy ceiling %+v past the capability's", c)
			}
		}
	})
}
