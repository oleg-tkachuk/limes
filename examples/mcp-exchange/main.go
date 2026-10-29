// Command mcp-exchange runs an MCP server behind standard OAuth that works
// with limes capabilities behind it. A client finds the server's
// authorization server through its protected resource metadata (RFC 9728),
// presents the identity provider's access token, and the server exchanges
// it for a capability to its tools (RFC 8693) and narrows that for a
// sub-agent. Everything runs in this process, over TLS on loopback.
//
//	go run ./mcp-exchange
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
	"uuid"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/examples/internal/demo"
	"github.com/oleg-tkachuk/limes/exchange"
	"github.com/oleg-tkachuk/limes/exchange/jwtverify"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

const (
	// The identity provider, its key, and the MCP client it issues tokens to.
	idpIssuer = "https://idp.example.com"
	idpKID    = "idp-key-1"
	mcpClient = "example-mcp-client"
	userName  = "user-42"
	tokenTTL  = 10 * time.Minute
	// The capability issuer, the tools' audience and the resource they serve.
	limesIssuer  = "example-exchange"
	toolsService = "https://tools.example.com"
	toolResource = "tools/search"
	// exchangeAudience is the exchange itself, which a capability it narrows
	// must name; subAgent is who the narrowed capability is for.
	exchangeAudience = "https://exchange.example.com"
	subAgent         = "search-sub-agent"
	// userRequests and subAgentRequests are the requests the user's and the
	// sub-agent's capabilities allow: a capability no request can use is
	// never issued.
	userRequests     = 100
	subAgentRequests = 10
	// mcpPath is where the MCP server answers.
	mcpPath     = "/mcp"
	bearer      = "Bearer "
	toolsScope  = "tools:read"
	metadataKey = "resource_metadata="
)

// errUnauthorized is a 401 from the MCP server, which the example expects
// for a token not issued for it.
var errUnauthorized = errors.New("401 Unauthorized")

// idp is an identity provider: a key and the key set it publishes.
type idp struct {
	key  ed25519.PrivateKey
	keys jwk.Set
}

func newIDP() (*idp, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pub, err := jwk.Import(key.Public())
	if err != nil {
		return nil, err
	}
	for name, value := range map[string]any{jwk.KeyIDKey: idpKID, jwk.AlgorithmKey: jwa.EdDSAEd25519()} {
		if err := pub.Set(name, value); err != nil {
			return nil, err
		}
	}
	set := jwk.NewSet()
	return &idp{key: key, keys: set}, set.AddKey(pub)
}

// accessToken is an RFC 9068 access token for audience, as the identity
// provider issues one after the user signs in through the MCP client.
func (p *idp) accessToken(audience string) (string, error) {
	now := time.Now()
	payload, err := json.Marshal(map[string]any{
		"iss": idpIssuer, "sub": userName, "aud": audience, "client_id": mcpClient,
		"iat": now.Unix(), "exp": now.Add(tokenTTL).Unix(), "jti": uuid.New().String(), "scope": toolsScope,
	})
	if err != nil {
		return "", err
	}
	headers := jws.NewHeaders()
	for name, value := range map[string]any{jws.TypeKey: jwtverify.TypeAccessToken, jws.KeyIDKey: idpKID} {
		if err := headers.Set(name, value); err != nil {
			return "", err
		}
	}
	token, err := jws.Sign(payload, jws.WithKey(jwa.EdDSAEd25519(), p.key, jws.WithProtectedHeaders(headers)))
	return string(token), err
}

// server is the MCP server: its exchanger, and the verifier its tools use.
type server struct {
	resource string
	ex       *exchange.Exchanger
	tools    *limes.Verifier
	log      *slog.Logger
	tenant   uuid.UUID
}

// serveMCP answers a tool call: it exchanges the caller's access token for a
// capability to the tools, narrows that for the sub-agent that runs the
// search, and has the tool authorize the sub-agent's call.
func (s *server) serveMCP(w http.ResponseWriter, r *http.Request) {
	metadataURL, _ := exchange.ResourceMetadataURL(s.resource)
	challenge := exchange.ResourceChallenge(metadataURL, []string{toolsScope})
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearer)
	if !ok {
		w.Header().Set("WWW-Authenticate", challenge)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	self := exchange.Client{ID: s.resource, SubjectAudiences: []string{s.resource}}
	res, err := s.ex.Exchange(r.Context(), exchange.Request{
		Client: self, SubjectToken: token, Resources: []string{toolsService, exchangeAudience},
	})
	if err != nil {
		w.Header().Set("WWW-Authenticate", challenge)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.log.Info("the MCP server exchanged the user's access token for a capability",
		"capability", res.Capability.ID, "subject", res.Capability.Subject.Subject, "audience", res.Capability.Audience,
		"expires", res.Capability.ExpiresAt.UTC().Format(time.RFC3339))
	child, err := s.ex.Narrow(r.Context(), exchange.NarrowRequest{Client: self, SubjectToken: res.Token, Resources: []string{toolsService}})
	if err != nil {
		http.Error(w, "narrow", http.StatusInternalServerError)
		return
	}
	s.log.Info("and narrowed it for the sub-agent that runs the search",
		"capability", child.Capability.ID, "parent", child.Capability.ParentID, "subject", child.Capability.Subject.Subject,
		"audience", child.Capability.Audience)
	if _, err := s.tools.Authorize(r.Context(), limes.AuthorizeRequest{
		Verify: limes.VerifyRequest{Token: child.Token, Audience: toolsService},
		Check:  limes.CheckRequest{Op: limes.OpGet, Resource: toolResource},
	}); err != nil {
		http.Error(w, "tool refused", http.StatusForbidden)
		return
	}
	_, _ = fmt.Fprintf(w, "search ran for %s", res.Capability.Subject.Subject)
}

// policy gives the token's user the right to read the tools and to hand
// that on to a sub-agent.
func (s *server) policy(_ context.Context, g exchange.Grant) (limes.IssueRequest, error) {
	return limes.IssueRequest{
		IssuedBy: limes.Principal{Type: limes.PrincipalTypeService, Subject: s.resource},
		Subject:  limes.Principal{Type: limes.PrincipalTypeUser, TenantID: s.tenant, Subject: g.Subject.Subject},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet, limes.OpShare}, ResourcePrefixes: []string{"tools/"},
			Requests: limes.RequestCeiling{Max: userRequests},
		},
	}, nil
}

// narrowPolicy gives the sub-agent the search alone.
func (s *server) narrowPolicy(context.Context, exchange.Narrowing) (limes.DelegateRequest, error) {
	return limes.DelegateRequest{
		Subject: limes.Principal{Type: limes.PrincipalTypeAgent, Subject: subAgent},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, ResourcePrefixes: []string{toolResource},
			Requests: limes.RequestCeiling{Max: subAgentRequests},
		},
	}, nil
}

func main() {
	log := demo.New("mcp-exchange")
	must := func(err error, step string) { demo.Must(log, err, step) }
	ctx := context.Background()

	provider, err := newIDP()
	must(err, "identity provider")
	key, err := limes.GenerateKey()
	must(err, "signing key")
	records, err := memstore.NewStore(memstore.StoreConfig{})
	must(err, "record store")
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	must(err, "signer")
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: limesIssuer})
	must(err, "issuer")
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	must(err, "key set")
	verifier, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: records, TrustedIssuers: []string{limesIssuer}})
	must(err, "verifier")
	subjects, err := jwtverify.New(jwtverify.Config{Issuer: idpIssuer, Keys: jwtverify.StaticKeys(provider.keys)})
	must(err, "subject verifier")

	s := &server{tools: verifier, log: log, tenant: uuid.New()}
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	s.resource = srv.URL + mcpPath
	s.ex, err = exchange.NewExchanger(exchange.Config{
		Issuer: issuer, Subjects: subjects, Policy: s.policy,
		Capabilities: verifier, CapabilityAudience: exchangeAudience, NarrowPolicy: s.narrowPolicy,
		Logger: slog.New(slog.DiscardHandler),
	})
	must(err, "exchanger")
	metadata, err := exchange.NewResourceMetadataHandler(exchange.ResourceMetadata{
		Resource: s.resource, AuthorizationServers: []string{idpIssuer}, ScopesSupported: []string{toolsScope},
	})
	must(err, "resource metadata")
	metadataURL, err := exchange.ResourceMetadataURL(s.resource)
	must(err, "resource metadata URL")
	mux.Handle(strings.TrimPrefix(metadataURL, srv.URL), metadata)
	mux.HandleFunc("POST "+mcpPath, s.serveMCP)
	client := srv.Client()

	// 1. The client calls without a token, and is pointed at the metadata.
	resp, err := call(ctx, client, s.resource, "")
	must(err, "call without a token")
	challenge := resp.header.Get("WWW-Authenticate")
	if resp.status != http.StatusUnauthorized || !strings.Contains(challenge, metadataKey) {
		must(fmt.Errorf("status %d, challenge %q", resp.status, challenge), "a 401 naming the metadata")
	}
	log.Info("the MCP server answered a call without a token with a 401 naming its metadata", "WWW-Authenticate", challenge)

	// 2. It reads the metadata, which names the authorization server.
	var doc exchange.ResourceMetadata
	must(getJSON(ctx, client, metadataURL, &doc), "read the resource metadata")
	log.Info("the client read the protected resource metadata", "resource", doc.Resource, "authorization servers", doc.AuthorizationServers)

	// 3. The user signs in there; the client calls again with the token.
	token, err := provider.accessToken(doc.Resource)
	must(err, "access token")
	resp, err = call(ctx, client, s.resource, token)
	must(err, "call with the access token")
	if resp.status != http.StatusOK {
		must(fmt.Errorf("status %d: %s", resp.status, resp.body), "the tool call")
	}
	log.Info("the tool call succeeded on the sub-agent's capability", "answer", resp.body)

	// 4. A token issued for another resource is not accepted.
	elsewhere, err := provider.accessToken("https://other-mcp.example.com")
	must(err, "access token for another server")
	resp, err = call(ctx, client, s.resource, elsewhere)
	must(err, "call with another server's token")
	if resp.status == http.StatusUnauthorized {
		err = errUnauthorized
	}
	demo.Refused(log, err, errUnauthorized, "a token issued for another MCP server")
}

// reply is an MCP server's answer, read whole.
type reply struct {
	status int
	header http.Header
	body   string
}

// call POSTs to the MCP endpoint, with token when there is one.
func call(ctx context.Context, client *http.Client, url, token string) (reply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","id":1}`))
	if err != nil {
		return reply{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", bearer+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: string(body)}, err
}

// getJSON GETs url and decodes its body into v.
func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
