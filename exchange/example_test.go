package exchange_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/exchange"
	"github.com/oleg-tkachuk/limes/memstore"
)

const (
	// exampleIDP is the identity provider that issued the subject tokens.
	exampleIDP = "https://idp.example.com"
	// exampleMCPServer is the exchanging MCP server: the audience of the
	// tokens it receives, and the client of the exchange.
	exampleMCPServer = "https://mcp.example.com"
	// exampleTool is the resource the capability is asked for.
	exampleTool = "https://tools.example.com"
	// exampleSubjectToken is the one subject token the examples' verifier
	// knows.
	exampleSubjectToken = "opaque-access-token"
	// exampleSubjectTTL is the subject token's remaining lifetime.
	exampleSubjectTTL = 5 * time.Minute
	// exampleEndpoint is the token endpoint's path.
	exampleEndpoint = "/token"
	// formContentType is a token request's media type (RFC 6749 §3.2).
	formContentType = "application/x-www-form-urlencoded"
)

// exampleNow is the examples' fixed clock.
var exampleNow = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)

func exampleClock() time.Time { return exampleNow }

// opaqueSubjects is a SubjectVerifier of a consumer's own, over opaque
// tokens it looked up already (by RFC 7662 introspection, say): package
// exchange links no JOSE library, and exchange/jwtverify is the one for JWT
// access tokens.
type opaqueSubjects map[string]exchange.Subject

func (s opaqueSubjects) VerifySubject(_ context.Context, token string) (exchange.Subject, error) {
	subject, ok := s[token]
	if !ok {
		// A token refused is ErrInvalidGrant; any other error is the server's.
		return exchange.Subject{}, exchange.ErrInvalidGrant
	}
	return subject, nil
}

// newExampleExchanger is an Exchanger over an in-memory store whose policy
// lets the token's user read, with the scopes asked for.
func newExampleExchanger() *exchange.Exchanger {
	key, err := limes.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		log.Fatal(err)
	}
	records, err := memstore.NewStore(memstore.StoreConfig{Now: exampleClock})
	if err != nil {
		log.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: "limes-exchange", Now: exampleClock})
	if err != nil {
		log.Fatal(err)
	}
	tenant := uuid.New()
	subjects := opaqueSubjects{exampleSubjectToken: {
		Issuer: exampleIDP, Subject: "user-1", ClientID: "mcp-client", Audience: []string{exampleMCPServer},
		Scopes: []string{"tools:read"}, IssuedAt: exampleNow, ExpiresAt: exampleNow.Add(exampleSubjectTTL),
	}}
	// The policy decides what each grant issues: there is none by default.
	policy := func(_ context.Context, g exchange.Grant) (exchange.Decision, error) {
		return exchange.Decision{Issue: limes.IssueRequest{
			IssuedBy: limes.Principal{Type: limes.PrincipalTypeService, Subject: exampleMCPServer},
			Subject:  limes.Principal{Type: limes.PrincipalTypeUser, TenantID: tenant, Subject: g.Subject.Subject},
			Caveats:  limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet}},
		}, Scopes: g.Scopes}, nil
	}
	ex, err := exchange.NewExchanger(exchange.Config{
		Issuer: issuer, Subjects: subjects, Policy: policy,
		Now: exampleClock, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		log.Fatal(err)
	}
	return ex
}

// exampleClient is the MCP server as a client of the exchange: it presents
// only tokens issued to itself.
var exampleClient = exchange.Client{ID: exampleMCPServer, SubjectAudiences: []string{exampleMCPServer}}

// An MCP server exchanges the access token its client sent for a capability
// to the tool it calls, in process.
func ExampleExchanger_Exchange() {
	ctx := context.Background()
	ex := newExampleExchanger()

	res, err := ex.Exchange(ctx, exchange.Request{
		Client: exampleClient, SubjectToken: exampleSubjectToken,
		Resources: []string{exampleTool}, Scopes: []string{"tools:read"},
	})
	if err != nil {
		log.Fatal(err)
	}
	c := res.Capability
	fmt.Println(res.TokenType, c.Audience, c.Subject.Subject, c.ClientID, res.Scopes)
	// The capability ends no later than the subject token.
	fmt.Println(c.ExpiresAt.Sub(exampleNow))

	// A token issued to another client is refused, as the token endpoint
	// would answer it.
	_, err = ex.Exchange(ctx, exchange.Request{
		Client:       exchange.Client{ID: "other-server", SubjectAudiences: []string{"https://other.example.com"}},
		SubjectToken: exampleSubjectToken, Resources: []string{exampleTool},
	})
	refusal, _ := exchange.ErrorOf(err)
	fmt.Println(errors.Is(err, exchange.ErrInvalidGrant), refusal.Status, refusal.Code)
	// Output:
	// Bearer [https://tools.example.com] user-1 mcp-client [tools:read]
	// 5m0s
	// true 400 invalid_grant
}

// A gateway serves the exchange as an RFC 8693 token endpoint.
func ExampleNewHandler() {
	h, err := exchange.NewHandler(exchange.HandlerConfig{
		Exchanger: newExampleExchanger(),
		// Authenticate the client as the deployment does: mTLS, a client
		// assertion, a shared secret.
		Authenticate: func(*http.Request) (exchange.Client, error) { return exampleClient, nil },
		Logger:       slog.New(slog.DiscardHandler),
	})
	if err != nil {
		log.Fatal(err)
	}
	form := url.Values{
		"grant_type":         {exchange.GrantTypeTokenExchange},
		"subject_token":      {exampleSubjectToken},
		"subject_token_type": {exchange.TokenTypeAccessToken},
		"resource":           {exampleTool},
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, exampleEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", formContentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var body struct {
		TokenType       string `json:"token_type"`
		IssuedTokenType string `json:"issued_token_type"`
		ExpiresIn       int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		log.Fatal(err)
	}
	fmt.Println(rec.Code, rec.Header().Get("Cache-Control"))
	fmt.Println(body.TokenType, body.IssuedTokenType == exchange.IssuedTokenTypeCapability, body.ExpiresIn)
	// Output:
	// 200 no-store
	// Bearer true 300
}

func ExampleErrorOf() {
	// An in-process caller answers a refusal as the token endpoint would.
	e, ok := exchange.ErrorOf(fmt.Errorf("verify subject: %w", exchange.ErrInvalidGrant))
	fmt.Println(ok, e.Status, e.Code)

	// An error that is none of the exchange's is the server's failure.
	_, ok = exchange.ErrorOf(errors.New("database unreachable"))
	fmt.Println(ok)
	// Output:
	// true 400 invalid_grant
	// false
}

// An MCP server tells a client that sent no token where its resource
// metadata is (RFC 9728), and so which authorization server to ask.
func ExampleResourceChallenge() {
	metadataURL, err := exchange.ResourceMetadataURL("https://mcp.example.com/mcp")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(metadataURL)
	fmt.Println(exchange.ResourceChallenge(metadataURL, []string{"tools:read"}))
	// Output:
	// https://mcp.example.com/.well-known/oauth-protected-resource/mcp
	// Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", scope="tools:read"
}
