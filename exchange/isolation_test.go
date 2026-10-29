package exchange

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Package exchange links no JOSE library: a consumer that verifies subject
// tokens on a stack of its own implements SubjectVerifier, and links only
// that stack. The verifier over jwx is exchange/jwtverify's
// (docs/adr/0010-exchange-jose-subpackage.md).

// exchangePackage is this package's import path, which the listing must
// hold, so an empty or wrong-directory `go list` cannot pass as clean.
const exchangePackage = "github.com/oleg-tkachuk/limes/exchange"

// forbiddenJOSE are the import-path prefixes of the JOSE libraries
// package exchange must not depend on, matched on path-segment boundaries.
var forbiddenJOSE = []string{
	"github.com/lestrrat-go",
	"github.com/go-jose/go-jose",
	"gopkg.in/square/go-jose.v2",
	"github.com/golang-jwt/jwt",
}

// joseIn is every path of paths under a prefix of forbiddenJOSE.
func joseIn(paths []string) []string {
	var found []string
	for _, p := range paths {
		if slices.ContainsFunc(forbiddenJOSE, func(prefix string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }) {
			found = append(found, p)
		}
	}
	return found
}

func TestJOSEIn(t *testing.T) {
	got := joseIn([]string{"fmt", "github.com/lestrrat-go/jwx/v3/jwt", "github.com/lestrrat-go-other", "github.com/go-jose/go-jose/v4"})
	want := []string{"github.com/lestrrat-go/jwx/v3/jwt", "github.com/go-jose/go-jose/v4"}
	if !slices.Equal(got, want) {
		t.Errorf("joseIn = %q, want %q", got, want)
	}
}

// The package's dependency graph, test files left out, holds no JOSE
// library.
func TestExchangeLinksNoJOSELibrary(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := strings.Fields(string(out))
	if !slices.Contains(deps, exchangePackage) {
		t.Fatalf("go list -deps did not list %s: %q", exchangePackage, deps)
	}
	if found := joseIn(deps); len(found) > 0 {
		t.Errorf("package exchange depends on a JOSE library: %q; it belongs in exchange/jwtverify", found)
	}
}
