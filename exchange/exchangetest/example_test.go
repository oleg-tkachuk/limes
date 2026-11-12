package exchangetest_test

import (
	"context"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/exchange"
	"github.com/oleg-tkachuk/limes/exchange/exchangetest"
)

// opaqueVerifier is a SubjectVerifier of a consumer's own, over opaque
// tokens it looks up (by RFC 7662 introspection, say): here, a fixed table.
type opaqueVerifier map[string]exchange.Subject

func (v opaqueVerifier) VerifySubject(_ context.Context, token string) (exchange.Subject, error) {
	s, ok := v[token]
	if !ok {
		return exchange.Subject{}, exchange.ErrInvalidGrant
	}
	return s, nil
}

// A SubjectVerifier's own test file holds it to what the Exchanger relies
// on. TestSubjectVerifier takes the test's *testing.T, so the example shows
// the test function rather than running it. A refusal the verifier cannot
// be shown is left nil, and skipped.
func ExampleTestSubjectVerifier() {
	const validToken = "opaque-token-1"
	want := exchange.Subject{
		Issuer: "https://idp.example.com", Subject: "user-1", ClientID: "mcp-client",
		Audience: []string{"https://mcp.example.com"}, ExpiresAt: time.Now().Add(time.Hour),
	}
	v := opaqueVerifier{validToken: want}
	_ = func(t *testing.T) {
		exchangetest.TestSubjectVerifier(t, v, exchangetest.Tokens{
			Valid:  func(*testing.T) (string, exchange.Subject) { return validToken, want },
			Forged: func(*testing.T) string { return "opaque-token-unknown" },
		})
	}
}
