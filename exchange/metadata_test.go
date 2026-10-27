package exchange

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/oleg-tkachuk/limes/dpop"
)

// The well-known URL sits between a URL's host and its path, which keeps
// its own path and loses a trailing slash (RFC 9728 §3.1, RFC 8414 §3.1).
func TestWellKnownURLs(t *testing.T) {
	for in, want := range map[string]string{
		"https://mcp.example.com":            "https://mcp.example.com/.well-known/oauth-protected-resource",
		"https://mcp.example.com/":           "https://mcp.example.com/.well-known/oauth-protected-resource",
		"https://mcp.example.com/server/mcp": "https://mcp.example.com/.well-known/oauth-protected-resource/server/mcp",
		"https://mcp.example.com:8443/mcp":   "https://mcp.example.com:8443/.well-known/oauth-protected-resource/mcp",
	} {
		if got, err := ResourceMetadataURL(in); err != nil || got != want {
			t.Errorf("ResourceMetadataURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if got, err := AuthorizationServerMetadataURL("https://exchange.example.com/tenant-1"); err != nil ||
		got != "https://exchange.example.com/.well-known/oauth-authorization-server/tenant-1" {
		t.Errorf("AuthorizationServerMetadataURL = %q, %v", got, err)
	}
	for _, bad := range []string{"http://mcp.example.com", "mcp.example.com", "https://mcp.example.com#x", "https:///mcp"} {
		if _, err := ResourceMetadataURL(bad); err == nil {
			t.Errorf("ResourceMetadataURL(%q) was accepted", bad)
		}
	}
	if _, err := AuthorizationServerMetadataURL(idpIssuer + "?x=1"); err == nil {
		t.Error("an issuer with a query was accepted")
	}
}

// getJSON is h's answer to a GET, decoded.
func getJSON(t *testing.T, h http.Handler) (map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	return doc, w
}

// The resource metadata names the resource and its authorization servers,
// and the header as the one way to present a token.
func TestResourceMetadataHandler(t *testing.T) {
	h, err := NewResourceMetadataHandler(ResourceMetadata{
		Resource: mcpServer, AuthorizationServers: []string{idpIssuer}, DPoPSigningAlgValuesSupported: dpop.SigningAlgorithms(),
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, w := getJSON(t, h)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != jsonContentType || w.Header().Get("Cache-Control") == "" {
		t.Errorf("status %d, headers %v", w.Code, w.Header())
	}
	if doc["resource"] != mcpServer || !slices.Equal(anyStrings(doc["authorization_servers"]), []string{idpIssuer}) ||
		!slices.Equal(anyStrings(doc["bearer_methods_supported"]), []string{BearerMethodHeader}) ||
		!slices.Equal(anyStrings(doc["dpop_signing_alg_values_supported"]), dpop.SigningAlgorithms()) {
		t.Errorf("document %v", doc)
	}
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d", post.Code)
	}
	for name, md := range map[string]ResourceMetadata{
		"no resource":           {AuthorizationServers: []string{idpIssuer}},
		"an http resource":      {Resource: "http://mcp.example.com", AuthorizationServers: []string{idpIssuer}},
		"no server":             {Resource: mcpServer},
		"a server with a query": {Resource: mcpServer, AuthorizationServers: []string{idpIssuer + "?x=1"}},
	} {
		if _, err := NewResourceMetadataHandler(md); err == nil {
			t.Errorf("a document with %s was served", name)
		}
	}
}

// The exchange's metadata is an authorization server's with a token
// endpoint, token exchange its one grant and no response type.
func TestAuthorizationServerMetadataHandler(t *testing.T) {
	h, err := NewAuthorizationServerMetadataHandler(AuthorizationServerMetadata{Issuer: exchangeAudience, TokenEndpoint: tokenEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := getJSON(t, h)
	if doc["issuer"] != exchangeAudience || doc["token_endpoint"] != tokenEndpoint ||
		!slices.Equal(anyStrings(doc["grant_types_supported"]), []string{GrantTypeTokenExchange}) {
		t.Errorf("document %v", doc)
	}
	if types, ok := doc["response_types_supported"].([]any); !ok || len(types) != 0 {
		t.Errorf("response_types_supported = %v, want an empty array", doc["response_types_supported"])
	}
	if _, err := NewAuthorizationServerMetadataHandler(AuthorizationServerMetadata{Issuer: exchangeAudience}); err == nil {
		t.Error("a document with no token endpoint was served")
	}
}

// The challenge names the metadata and the scopes, each a quoted-string.
func TestResourceChallenge(t *testing.T) {
	const metadata = "https://mcp.example.com/.well-known/oauth-protected-resource"
	if got, want := ResourceChallenge(metadata, []string{"tools:read", "tools:write"}),
		`Bearer resource_metadata="`+metadata+`", scope="tools:read tools:write"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got, want := ResourceChallenge(`https://x/"a\b`, nil), `Bearer resource_metadata="https://x/\"a\\b"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// anyStrings is a JSON array of strings as a []string.
func anyStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}
