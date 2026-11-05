package limes

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// docAnchor is an anchor of errorsDoc, as a problem type's fragment names it.
var docAnchor = regexp.MustCompile(`<a id="([^"]+)"></a>`)

// Every problem type limes answers with is an anchor of docs/errors.md,
// once: a code, a refusal's reason and a family's type, so each type URI
// resolves to the row that says what it means.
func TestEveryProblemTypeIsAnAnchorOfTheErrorsDoc(t *testing.T) {
	src, err := os.ReadFile(errorsDoc)
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]int{}
	for _, m := range docAnchor.FindAllStringSubmatch(string(src), -1) {
		anchors[m[1]]++
	}
	types := map[string]bool{}
	for _, c := range Codes() {
		types[string(c)] = true
	}
	for _, r := range Reasons() {
		types[string(r)] = true
	}
	for _, r := range []refusal{answerKeysUnavailable, answerMalformed, answerNoCredentials, answerNonce, answerProof, answerToken, answerScope, answerExhausted, answerConflict, internalError} {
		types[r.slug] = true
	}
	for typ := range types {
		uri := ProblemTypeURI(typ)
		fragment, ok := strings.CutPrefix(uri, problemTypeBase)
		if !ok || fragment != typ {
			t.Errorf("ProblemTypeURI(%q) = %q, want %s%s", typ, uri, problemTypeBase, typ)
		}
		if anchors[typ] != 1 {
			t.Errorf("%s has %d anchors %q, want 1: add <a id=%q></a> to its row", errorsDoc, anchors[typ], typ, typ)
		}
	}
}
