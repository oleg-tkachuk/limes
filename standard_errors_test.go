package limes

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/internal/errguard"
)

// wrappedHelpers are the functions of this module that construct an error
// naming no sentinel, each because every caller wraps it in a family at the
// API's edge: their text is kept as the cause the family's message names.
// A construction anywhere else names a sentinel, or fails here.
var wrappedHelpers = map[string]map[string]string{
	"internal/jose/jose.go": {
		"UnB64": "decoding helpers: the verifier, inspect and dpop wrap their errors in ErrInvalidToken or dpop.ErrInvalid",
		"Split": "", "PublicJWK": "", "Thumbprint": "", "PublicKey": "", "ed25519Key": "", "p256Key": "", "VerifyEd25519": "",
	},
	"internal/rfc3986/rfc3986.go": {
		"errNotAbsolute": "dpop wraps a URI it cannot normalise in ErrInvalidRequest, or answers the match false",
		"errUserinfo":    "",
	},
	"jwks/remote.go": {
		"sameOrigin": "a fetch's failures: Remote returns them inside ErrUnavailable",
		"fetch":      "", "readKeys": "", "readCapped": "",
	},
	"sealed_form.go": {
		"applyAttenuation": "a Biscuit's blocks: unsealedGrant refuses with ErrBiscuitAttenuation or ErrInvalidToken",
		"copyAxisNarrows":  "", "checkUnsealed": "",
	},
	"signer.go": {
		"priorActors":            "reading claims: readSignedClaims refuses with ErrInvalidToken, inspect.Decode too",
		"unmarshalCanonicalUUID": "", "parseHeader": "", "decodeClaims": "", "readConfirmation": "", "readParentID": "",
		"readActing": "", "checkActingForItself": "", "readOnBehalfOf": "", "canonicalUUID": "",
	},
	"metertest/metertest.go": {
		"checkCallbackRollback": "a conformance check's own fixture: the error a side effect fails with",
	},
}

// nestedModuleFile marks a directory as a module of its own, checked by its
// own test.
const nestedModuleFile = "go.mod"

// moduleSources are this module's non-test Go files, nested modules,
// examples and tools left out.
func moduleSources(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == "." {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, nestedModuleFile)); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Every error this module constructs names a sentinel, so it has a code
// (CodeOf, ProblemOf), but for the helpers listed above.
func TestEveryErrorNamesASentinel(t *testing.T) {
	sites, err := errguard.Bare(moduleSources(t))
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, s := range sites {
		if _, ok := wrappedHelpers[s.File][s.Func]; ok {
			used[s.File+":"+s.Func] = true
			continue
		}
		t.Errorf("%s:%d (%s) constructs an error that names no sentinel: wrap ErrInvalidRequest, ErrInvalidConfig, ErrInternal or the refusal it is", s.File, s.Line, s.Func)
	}
	for file, funcs := range wrappedHelpers {
		for fn := range funcs {
			if !used[file+":"+fn] {
				t.Errorf("%s: %s is listed as a wrapped helper and constructs no bare error: drop it", file, fn)
			}
		}
	}
}
