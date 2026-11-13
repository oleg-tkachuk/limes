package exchange

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// isNQChar reports whether c is a byte RFC 6749 §3.3 lets a scope token hold.
func isNQChar(c byte) bool {
	return c >= nqcharFirst && c <= nqcharLast && c != nqcharQuote && c != nqcharSlash
}

// FuzzParseScope holds the scope reader to RFC 6749 §3.3 on whatever a
// client sends: it accepts a scope only when every byte but the spaces
// between tokens is one a token may hold, and then reads the tokens as
// they were sent, none empty, the spaces between them dropped; any other
// scope is ErrInvalidScope.
func FuzzParseScope(f *testing.F) {
	for _, s := range []string{"read write", "  read   write ", "", "a\tb", `a"b`, `a\b`, "é", "read\nwrite"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		scopes, err := parseScope(raw)
		valid := true
		for i := range len(raw) {
			if raw[i] != scopeDelimiter[0] && !isNQChar(raw[i]) {
				valid = false
			}
		}
		if !valid {
			if !errors.Is(err, ErrInvalidScope) {
				t.Fatalf("%q: err = %v, want ErrInvalidScope", raw, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%q: a scope of allowed bytes refused: %v", raw, err)
		}
		for _, s := range scopes {
			if s == "" || strings.Contains(s, scopeDelimiter) {
				t.Fatalf("%q: read token %q", raw, s)
			}
		}
		if got, want := strings.Join(scopes, scopeDelimiter), strings.Join(strings.Fields(raw), scopeDelimiter); got != want {
			t.Fatalf("%q read as %q, want %q", raw, got, want)
		}
	})
}

// FuzzParseErrorResponse holds the reader of a token endpoint's answer to
// what it promises a client of an endpoint it may not trust: nil for a
// success, and for anything else a *ResponseError of the status sent, its
// sentinel the code's when the code is one it knows, ErrServerError
// otherwise, and answered by limes.ProblemFor with a status — never a panic,
// whatever the body.
func FuzzParseErrorResponse(f *testing.F) {
	f.Add(http.StatusBadRequest, []byte(`{"error":"invalid_grant","error_description":"no"}`))
	f.Add(http.StatusBadRequest, []byte(`{"error":"use_dpop_nonce"}`))
	f.Add(http.StatusInternalServerError, []byte(`<html>`))
	f.Add(http.StatusOK, []byte(`{}`))
	f.Fuzz(func(t *testing.T, status int, body []byte) {
		resp := &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body))}
		err := ParseErrorResponse(resp)
		if status >= http.StatusOK && status < http.StatusMultipleChoices {
			if err != nil {
				t.Fatalf("a %d answer: %v, want nil", status, err)
			}
			return
		}
		var re *ResponseError
		if !errors.As(err, &re) {
			t.Fatalf("a %d answer: %T %v, want a *ResponseError", status, err, err)
		}
		if re.Response.Status != status {
			t.Fatalf("a %d answer read as status %d", status, re.Response.Status)
		}
		if want, known := responseSentinels[re.Response.Code]; known && !errors.Is(err, want) && !errors.Is(err, ErrServerError) {
			t.Fatalf("code %q answered as %v, want its sentinel or ErrServerError", re.Response.Code, err)
		}
		if p := limes.ProblemFor(err); p.Status == 0 || p.Code == "" {
			t.Fatalf("a %d answer of code %q: ProblemFor = %+v, no status or code", status, re.Response.Code, p)
		}
	})
}
