package exchange

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes/dpop"
)

// exchangeSentinels are the package's exported sentinels, by name.
var exchangeSentinels = map[string]error{
	"ErrInvalidRequest": ErrInvalidRequest, "ErrInvalidClient": ErrInvalidClient,
	"ErrUnauthorizedClient": ErrUnauthorizedClient, "ErrServerError": ErrServerError,
	"ErrInvalidGrant": ErrInvalidGrant, "ErrInvalidTarget": ErrInvalidTarget,
	"ErrInvalidScope": ErrInvalidScope, "ErrInvalidAuthorizationDetails": ErrInvalidAuthorizationDetails,
	"ErrUnsupportedGrantType": ErrUnsupportedGrantType,
}

// declaredSentinels are the exported Err variables this package's non-test
// files declare.
func declaredSentinels(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					for _, n := range spec.(*ast.ValueSpec).Names {
						if strings.HasPrefix(n.Name, "Err") && n.IsExported() {
							names = append(names, n.Name)
						}
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// Every sentinel of the exchange, and every proof refusal it passes on, is
// answered with an RFC 6749 §5.2 error of its own rule.
func TestEverySentinelHasAnError(t *testing.T) {
	var listed []string
	for name := range exchangeSentinels {
		listed = append(listed, name)
	}
	sort.Strings(listed)
	if declared := declaredSentinels(t); !slices.Equal(declared, listed) {
		t.Fatalf("the sources declare %q; the test lists %q", declared, listed)
	}
	all := merged(exchangeSentinels, map[string]error{
		"dpop.ErrNonceRequired": dpop.ErrNonceRequired, "dpop.ErrRequired": dpop.ErrRequired,
		"dpop.ErrInvalid": dpop.ErrInvalid, "dpop.ErrReplayed": dpop.ErrReplayed,
	})
	for name, err := range all {
		got, ok := ErrorOf(fmt.Errorf("context: %w", err))
		if !ok || got.Code == "" || got.Status == 0 || got.Description == "" {
			t.Errorf("%s: ErrorOf = %+v, %v; want a code, a status and a description", name, got, ok)
		}
	}
	if len(refusals) != len(all) {
		t.Errorf("%d rules for %d sentinels", len(refusals), len(all))
	}
}

// merged is a and b in one map.
func merged(a, b map[string]error) map[string]error {
	out := map[string]error{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestErrorOf(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"a refused subject token": {ErrInvalidGrant, http.StatusBadRequest, codeInvalidGrant},
		"another target":          {fmt.Errorf("x: %w", ErrInvalidTarget), http.StatusBadRequest, codeInvalidTarget},
		"a client unknown":        {ErrInvalidClient, http.StatusUnauthorized, codeInvalidClient},
		"a nonce required":        {dpop.ErrNonceRequired, http.StatusBadRequest, codeUseDPoPNonce},
		"the server failing":      {ErrServerError, http.StatusInternalServerError, codeServerError},
	} {
		if got, ok := ErrorOf(tc.err); !ok || got.Status != tc.status || got.Code != tc.code {
			t.Errorf("%s: ErrorOf = %+v, %v; want %d %s", name, got, ok, tc.status, tc.code)
		}
	}
	if got, ok := ErrorOf(errors.New("a store's own")); ok {
		t.Errorf("ErrorOf of an error not the exchange's = %+v", got)
	}
	if codes := ErrorCodes(); !slices.Contains(codes, codeInvalidGrant) || slices.Contains(codes, "") {
		t.Errorf("ErrorCodes = %q", codes)
	}
}

// docRow reads a row of docs/errors.md's tables: its code and HTTP status.
var docRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| ([0-9]{3}) \\|")

// docs/errors.md lists every error the exchange answers with, with its
// status.
func TestErrorsDocListsEveryExchangeError(t *testing.T) {
	raw, err := os.ReadFile("../docs/errors.md")
	if err != nil {
		t.Fatal(err)
	}
	section := string(raw)
	if i := strings.Index(section, exchangeSection); i >= 0 {
		section = section[i:]
	} else {
		t.Fatalf("docs/errors.md has no %q section", exchangeSection)
	}
	doc := map[string]string{}
	for _, m := range docRow.FindAllStringSubmatch(section, -1) {
		doc[m[1]] = m[2]
	}
	for _, rule := range refusals {
		if got := doc[rule.code]; got != strconv.Itoa(rule.status) {
			t.Errorf("docs/errors.md lists %q with status %q; ErrorOf answers %d", rule.code, got, rule.status)
		}
	}
}

// exchangeSection heads the exchange's table in docs/errors.md.
const exchangeSection = "## The exchange's codes"
