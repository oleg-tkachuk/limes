package exchange

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// respond is the response the token endpoint writes for answer.
func respond(t *testing.T, answer Error) *http.Response {
	t.Helper()
	w := httptest.NewRecorder()
	writeJSON(w, answer.Status, answer, slog.Default())
	return w.Result()
}

// Every refusal the endpoint answers reads back, on the client's side, as
// the same answer: its code, its status, and limes's code and category of
// the sentinel its code stands for.
func TestParseErrorResponseRoundTrips(t *testing.T) {
	for _, rule := range refusals {
		answer, ok := ErrorOf(rule.err)
		if !ok {
			t.Fatalf("%v: no answer", rule.err)
		}
		resp := respond(t, answer)
		err := ParseErrorResponse(resp)
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		var re *ResponseError
		if !errors.As(err, &re) || re.Response != answer {
			t.Errorf("%s: read back %+v, want %+v", rule.code, re, answer)
			continue
		}
		back, ok := ErrorOf(err)
		if !ok || back.Code != answer.Code {
			t.Errorf("%s: ErrorOf of what the client read = %+v, %v", rule.code, back, ok)
		}
		want := responseSentinels[rule.code]
		code, _ := limes.CodeOf(err)
		wantCode, _ := limes.CodeOf(want)
		category, _ := limes.CategoryOf(err)
		wantCategory, _ := limes.CategoryOf(want)
		if !errors.Is(err, want) || code != wantCode || category != wantCategory {
			t.Errorf("%s: %v is %s %s, want %v: %s %s", rule.code, err, code, category, want, wantCode, wantCategory)
		}
	}
}

// Every code the endpoint answers with has the sentinel a client reads it
// as, and that sentinel answers with that code.
func TestEveryErrorCodeHasAResponseSentinel(t *testing.T) {
	for _, code := range ErrorCodes() {
		sentinel, ok := responseSentinels[code]
		if !ok {
			t.Errorf("%s: no sentinel a client reads it as", code)
			continue
		}
		if answer, _ := ErrorOf(sentinel); answer.Code != code {
			t.Errorf("%s: its sentinel answers %q", code, answer.Code)
		}
	}
	if len(responseSentinels) != len(ErrorCodes()) {
		t.Errorf("%d sentinels for %d codes", len(responseSentinels), len(ErrorCodes()))
	}
}

// A success is no error; a code this package does not know, a body that is
// no error response, and one past MaxErrorResponseBytes are the server's
// failure, the status kept.
func TestParseErrorResponseOfTheUnexpected(t *testing.T) {
	ok := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"t"}`))}
	if err := ParseErrorResponse(ok); err != nil {
		t.Errorf("a success: %v", err)
	}
	for name, body := range map[string]string{
		"an unknown code":    `{"error":"slow_down","error_description":"later"}`,
		"no JSON":            `<html>bad gateway</html>`,
		"an empty body":      ``,
		"a body past bounds": `{"error":"invalid_grant","error_description":"` + strings.Repeat("x", MaxErrorResponseBytes) + `"}`,
	} {
		resp := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(body))}
		err := ParseErrorResponse(resp)
		var re *ResponseError
		if !errors.Is(err, ErrServerError) || !errors.As(err, &re) || re.Response.Status != http.StatusBadGateway {
			t.Errorf("%s: %v, want ErrServerError keeping status %d", name, err, http.StatusBadGateway)
		}
	}
}

// What a real endpoint answers reads back as its refusal.
func TestParseErrorResponseOfTheHandler(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	w := post(t, newHandler(t, f), map[string][]string{paramGrantType: {"client_credentials"}}, nil)
	resp := w.Result()
	defer resp.Body.Close()
	err := ParseErrorResponse(resp)
	if !errors.Is(err, ErrUnsupportedGrantType) {
		t.Errorf("an unsupported grant: %v, want ErrUnsupportedGrantType", err)
	}
}
