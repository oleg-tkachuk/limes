// Package storable is the rule for text a store keeps: valid UTF-8 with no
// NUL. A PostgreSQL text or jsonb value holds nothing else — a parameter
// carrying either fails the whole statement, as the server's error rather
// than the caller's — and JSON encoding replaces invalid UTF-8, so a value
// kept that way is no longer the one written. Every request that hands a
// store text checks it by this rule, so each store refuses the same input.
//
// A released nested module builds against every later limes of its major,
// so what this package exports is kept buildable for as long as one does.
package storable

import (
	"strings"
	"unicode/utf8"
)

// nul is the one character valid UTF-8 carries and a store's text cannot.
const nul = "\x00"

// Text reports whether a store can keep every one of texts as it is.
func Text(texts ...string) bool {
	for _, s := range texts {
		if !utf8.ValidString(s) || strings.Contains(s, nul) {
			return false
		}
	}
	return true
}
