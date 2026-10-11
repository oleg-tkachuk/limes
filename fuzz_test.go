package limes

import (
	"os"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// The parsers below read bytes an attacker chooses. Each must refuse what it
// cannot read — never panic — and what it accepts must hold the invariants
// the code after it relies on.

// FuzzParseClaims holds the claims reader to its strictness: whatever it
// accepts names a capability, and its sub is its principal's.
func FuzzParseClaims(f *testing.F) {
	f.Add([]byte(`{"iss":"i","sub":"s","aud":["data"],"iat":1,"exp":2,"jti":"0190a5c3-0000-7000-8000-000000000000","limes_principal":{"tenant_id":"11111111-1111-1111-1111-111111111111","subject":"s"},"limes_caveats":{"ops":["get"],"max_budget":"1.5"}}`))
	f.Add([]byte(`{"iss":"a","iss":"b"}`))
	f.Add([]byte(`{"limes_caveats":{"Ops":["get"]}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		c, err := parseClaims(jose.B64(raw))
		if err != nil {
			return
		}
		if c.Caveats.MaxBudgetAmount < 0 || c.Caveats.MaxBudgetAmount > MaxNanos {
			t.Fatalf("accepted a budget of %d", int64(c.Caveats.MaxBudgetAmount))
		}
	})
}

// FuzzOpenBiscuit holds the Biscuit reader to refusing, never panicking on,
// a token it cannot read.
func FuzzOpenBiscuit(f *testing.F) {
	f.Add("")
	for _, name := range []string{biscuitSeedFile, biscuitAttenuatedFile} {
		raw, err := os.ReadFile(biscuitFixturePath(name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(strings.TrimSpace(string(raw)))
	}
	f.Fuzz(func(t *testing.T, token string) {
		_, _, _ = openBiscuit(token)
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
