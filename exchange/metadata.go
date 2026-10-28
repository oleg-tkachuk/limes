package exchange

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// The well-known paths the metadata documents are served under (RFC 9728
// §3, RFC 8414 §3), between a URL's host and its path.
const (
	WellKnownProtectedResource   = "/.well-known/oauth-protected-resource"
	WellKnownAuthorizationServer = "/.well-known/oauth-authorization-server"
)

// BearerMethodHeader is the one way an MCP client presents a token: the
// Authorization header (RFC 9728 §2, RFC 6750 §2.1).
const BearerMethodHeader = "header"

// MetadataMaxAge is how long a client may keep a metadata document.
const MetadataMaxAge = time.Hour

// schemeHTTPS is the scheme every identifier in the documents must have.
const schemeHTTPS = "https"

// ResourceMetadata is a protected resource's metadata (RFC 9728 §2), which
// an MCP server must publish: where its clients get tokens, and how they
// present them.
type ResourceMetadata struct {
	// Resource is the resource's identifier, the URL its clients name in
	// `resource` (RFC 8707). Required.
	Resource string `json:"resource"`
	// AuthorizationServers are the issuers its tokens come from: an MCP
	// client discovers its authorization server here. Required.
	AuthorizationServers []string `json:"authorization_servers"`
	// ScopesSupported are the scopes a client may ask for.
	ScopesSupported []string `json:"scopes_supported,omitempty"`
	// BearerMethodsSupported are how a token is presented. Empty:
	// BearerMethodHeader alone.
	BearerMethodsSupported []string `json:"bearer_methods_supported,omitempty"`
	// ResourceName and ResourceDocumentation describe it to people.
	ResourceName          string `json:"resource_name,omitempty"`
	ResourceDocumentation string `json:"resource_documentation,omitempty"`
	// DPoPSigningAlgValuesSupported are the algorithms its DPoP proofs may
	// use (RFC 9449 §5.1): dpop.SigningAlgorithms when it checks them.
	DPoPSigningAlgValuesSupported []string `json:"dpop_signing_alg_values_supported,omitempty"`
	// DPoPBoundAccessTokensRequired says it accepts only DPoP-bound tokens.
	DPoPBoundAccessTokensRequired bool `json:"dpop_bound_access_tokens_required,omitempty"`
}

// AuthorizationServerMetadata is the token exchange's metadata as an
// authorization server (RFC 8414 §2): one with a token endpoint and no
// authorization endpoint, issuing nothing but by token exchange.
type AuthorizationServerMetadata struct {
	// Issuer is its issuer identifier. Required.
	Issuer string `json:"issuer"`
	// TokenEndpoint is the Handler's URL. Required.
	TokenEndpoint string `json:"token_endpoint"`
	// JWKSURI is where the keys of the capabilities it issues are
	// published (jwks.NewHandler).
	JWKSURI string `json:"jwks_uri,omitempty"`
	// GrantTypesSupported is token exchange alone, and
	// ResponseTypesSupported none, there being no authorization endpoint;
	// both are set by the handler.
	GrantTypesSupported    []string `json:"grant_types_supported"`
	ResponseTypesSupported []string `json:"response_types_supported"`
	// TokenEndpointAuthMethodsSupported are how its clients authenticate
	// (HandlerConfig.Authenticate), such as "tls_client_auth".
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	// DPoPSigningAlgValuesSupported: dpop.SigningAlgorithms when the
	// exchange binds capabilities to keys.
	DPoPSigningAlgValuesSupported []string `json:"dpop_signing_alg_values_supported,omitempty"`
	// AuthorizationDetailsTypesSupported are the RFC 9396 types its policy
	// reads.
	AuthorizationDetailsTypesSupported []string `json:"authorization_details_types_supported,omitempty"`
}

// NewResourceMetadataHandler serves md, refusing a document whose resource
// or authorization servers are not https URLs without a fragment.
func NewResourceMetadataHandler(md ResourceMetadata) (http.Handler, error) {
	if err := checkHTTPSURL("ResourceMetadata.Resource", md.Resource); err != nil {
		return nil, err
	}
	if len(md.AuthorizationServers) == 0 {
		return nil, errors.New("exchange: ResourceMetadata.AuthorizationServers required")
	}
	for _, issuer := range md.AuthorizationServers {
		if err := checkIssuer("ResourceMetadata.AuthorizationServers", issuer); err != nil {
			return nil, err
		}
	}
	if len(md.BearerMethodsSupported) == 0 {
		md.BearerMethodsSupported = []string{BearerMethodHeader}
	}
	return metadataHandler(md)
}

// NewAuthorizationServerMetadataHandler serves md as the token exchange's
// metadata, setting its grant and response types, and refusing a document
// whose issuer or endpoints are not https URLs.
func NewAuthorizationServerMetadataHandler(md AuthorizationServerMetadata) (http.Handler, error) {
	if err := checkIssuer("AuthorizationServerMetadata.Issuer", md.Issuer); err != nil {
		return nil, err
	}
	if err := checkHTTPSURL("AuthorizationServerMetadata.TokenEndpoint", md.TokenEndpoint); err != nil {
		return nil, err
	}
	if md.JWKSURI != "" {
		if err := checkHTTPSURL("AuthorizationServerMetadata.JWKSURI", md.JWKSURI); err != nil {
			return nil, err
		}
	}
	md.GrantTypesSupported, md.ResponseTypesSupported = []string{GrantTypeTokenExchange}, []string{}
	return metadataHandler(md)
}

// metadataHandler serves doc as JSON to GET and HEAD.
func metadataHandler(doc any) (http.Handler, error) {
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("exchange: marshal metadata: %w", err)
	}
	cacheControl := "max-age=" + strconv.Itoa(int(MetadataMaxAge/time.Second))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", jsonContentType)
		w.Header().Set("Cache-Control", cacheControl)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	}), nil
}

// ResourceMetadataURL is where the metadata of the resource identified by
// resource is served: the well-known path between its host and its path
// (RFC 9728 §3.1).
func ResourceMetadataURL(resource string) (string, error) {
	if err := checkHTTPSURL("resource", resource); err != nil {
		return "", err
	}
	return wellKnown(resource, WellKnownProtectedResource), nil
}

// AuthorizationServerMetadataURL is where the metadata of the authorization
// server with issuer identifier issuer is served (RFC 8414 §3.1).
func AuthorizationServerMetadataURL(issuer string) (string, error) {
	if err := checkIssuer("issuer", issuer); err != nil {
		return "", err
	}
	return wellKnown(issuer, WellKnownAuthorizationServer), nil
}

// wellKnown is raw, a checked https URL, with path inserted between its
// host and its own path, which loses a trailing slash.
func wellKnown(raw, path string) string {
	u, _ := url.Parse(raw)
	u.Path = path + strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u.String()
}

// ResourceChallenge is the WWW-Authenticate value an MCP server answers a
// request that sent no token with: the Bearer scheme, the URL of its
// resource metadata, and the scopes the request needs, if any (RFC 9728
// §5.1, RFC 6750 §3). It names no error, as a request with no credentials
// gets none (RFC 6750 §3.1); a token the server refused is answered with
// the verifier's challenges instead, through WithResourceMetadata.
func ResourceChallenge(metadataURL string, scopes []string) string {
	params := []string{authParamResourceMetadata + "=" + quote(metadataURL)}
	if len(scopes) > 0 {
		params = append(params, "scope="+quote(strings.Join(scopes, " ")))
	}
	return "Bearer " + strings.Join(params, ", ")
}

// authParamResourceMetadata is the auth-param naming a protected resource's
// metadata URL in a challenge of any scheme (RFC 9728 §5.1).
const authParamResourceMetadata = "resource_metadata"

// WithResourceMetadata is resp, a verifier's answer to a request it refused,
// with the URL of the resource's metadata added to each challenge (RFC 9728
// §5.1): the error codes, the Bearer and DPoP schemes and the algorithms
// stay the verifier's (RFC 6750 §3, RFC 9449 §7.1). resp is not changed.
func WithResourceMetadata(resp limes.ErrorResponse, metadataURL string) limes.ErrorResponse {
	challenges := make([]string, len(resp.Challenges))
	for i, c := range resp.Challenges {
		challenges[i] = c + ", " + authParamResourceMetadata + "=" + quote(metadataURL)
	}
	resp.Challenges = challenges
	return resp
}

// quote is s as an RFC 9110 quoted-string.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// checkHTTPSURL refuses a value that is not an absolute https URL without a
// fragment.
func checkHTTPSURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != schemeHTTPS || u.Host == "" || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("exchange: %s %q is not an https URL without a fragment", name, raw)
	}
	return nil
}

// checkIssuer refuses an issuer identifier that is not an https URL without
// a query or a fragment (RFC 8414 §2).
func checkIssuer(name, raw string) error {
	if err := checkHTTPSURL(name, raw); err != nil {
		return err
	}
	if u, _ := url.Parse(raw); u.RawQuery != "" || strings.Contains(raw, "?") {
		return fmt.Errorf("exchange: %s %q has a query", name, raw)
	}
	return nil
}
