package inspect_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/inspect"
)

// FuzzInspect hands Decode and VerifySignature tokens the fuzzer writes,
// mutated from one an issuer signed, as a debugging tool is handed whatever
// was pasted into it: each refuses what it cannot read as
// limes.ErrInvalidToken, never a panic; Decode reads the signed token as the
// capability issued; and VerifySignature accepts that token alone, no other
// spelling of it.
func FuzzInspect(f *testing.F) {
	c, token, pub := issue(f)
	f.Add(token)
	f.Fuzz(func(t *testing.T, s string) {
		got, err := inspect.Decode(s)
		switch {
		case err != nil && !errors.Is(err, limes.ErrInvalidToken):
			t.Fatalf("Decode(%q) = %v, not ErrInvalidToken", s, err)
		case err == nil && s == token && !reflect.DeepEqual(got, c):
			t.Fatalf("Decode of the issued token = %+v, issued %+v", got, c)
		}
		err = inspect.VerifySignature(s, pub)
		switch {
		case err != nil && !errors.Is(err, limes.ErrInvalidToken):
			t.Fatalf("VerifySignature(%q) = %v, not ErrInvalidToken", s, err)
		case err == nil && s != token:
			t.Fatalf("VerifySignature accepted %q, which the issuer never signed", s)
		}
	})
}
