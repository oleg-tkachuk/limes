package limes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
)

// Issuer metadata, after OAuth 2.0 Authorization Server Metadata, RFC 8414.
//
// An issuer publishes one JSON document at a well-known URI derived from its
// issuer identifier (§3); a verifier configured with nothing but that
// identifier discovers where the keys are and which DPoP algorithms are
// accepted, and checks the document names the issuer it asked for (§3.3).

// wellKnownMetadata is RFC 8414 §3's well-known URI suffix, as registered by
// RFC 8615.
const wellKnownMetadata = "/.well-known/oauth-authorization-server"

// Discovery limits.
const (
	// defaultMetadataFetchTimeout bounds a discovery fetch made with no
	// client of the caller's.
	defaultMetadataFetchTimeout = 10 * time.Second
	// maxMetadataBytes caps a metadata document.
	maxMetadataBytes = 1 << 20
	// metadataMaxAge is how long a served document may be cached.
	metadataMaxAge = time.Hour
	// schemeHTTPS is the one scheme an issuer identifier may have (§2).
	schemeHTTPS = "https"
)

// ErrInvalidMetadata — a metadata document, or the issuer identifier it was
// fetched for, is not what RFC 8414 allows.
var ErrInvalidMetadata = errors.New("limes: invalid issuer metadata")

// IssuerMetadata is the part of RFC 8414 §2's metadata a capability issuer
// publishes. Unknown members are ignored when it is read (§3.2).
type IssuerMetadata struct {
	// Issuer is the issuer identifier: an https URL with no query and no
	// fragment, and the `iss` of every token the issuer signs.
	Issuer string `json:"issuer"`
	// JWKSURI is where the issuer's JWK Set is served (JWKSHandler).
	JWKSURI string `json:"jwks_uri"`
	// RevocationEndpoint is RFC 7009's endpoint (RevocationHandler).
	RevocationEndpoint string `json:"revocation_endpoint,omitempty"`
	// IntrospectionEndpoint is RFC 7662's endpoint (IntrospectionHandler).
	IntrospectionEndpoint string `json:"introspection_endpoint,omitempty"`
	// DPoPSigningAlgValuesSupported are the proof algorithms the issuer's
	// verifiers accept (RFC 9449 §5.1).
	DPoPSigningAlgValuesSupported []string `json:"dpop_signing_alg_values_supported,omitempty"`
	// AuthorizationDetailsTypesSupported are the RFC 9396 types the
	// issuer's tokens carry (RFC 9396 §10).
	AuthorizationDetailsTypesSupported []string `json:"authorization_details_types_supported,omitempty"`
}

// DPoPSigningAlgorithms are the JWS algorithms a DPoP proof may be signed
// with, as an issuer advertises them: the fully-specified names (RFC 9864).
// The deprecated "EdDSA" is still read, never advertised.
func DPoPSigningAlgorithms() []string { return []string{algEd25519, algES256} }

// NewIssuerMetadata is the metadata of an issuer identified by issuer,
// serving its keys at jwksURI: DPoP algorithms and the authorization details
// type filled in, the endpoints left for the caller to add.
func NewIssuerMetadata(issuer, jwksURI string) (IssuerMetadata, error) {
	m := IssuerMetadata{
		Issuer: issuer, JWKSURI: jwksURI,
		DPoPSigningAlgValuesSupported:      DPoPSigningAlgorithms(),
		AuthorizationDetailsTypesSupported: []string{AuthorizationDetailsType},
	}
	return m, m.Validate()
}

// Validate reports whether m is metadata RFC 8414 allows: an https issuer
// with no query or fragment, and an https jwks_uri.
func (m IssuerMetadata) Validate() error {
	if err := validateIssuerIdentifier(m.Issuer); err != nil {
		return err
	}
	for name, endpoint := range map[string]string{
		"jwks_uri": m.JWKSURI, "revocation_endpoint": m.RevocationEndpoint, "introspection_endpoint": m.IntrospectionEndpoint,
	} {
		if endpoint == "" && name != "jwks_uri" {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != schemeHTTPS || u.Host == "" {
			return fmt.Errorf("%w: %s %q is not an https URL", ErrInvalidMetadata, name, endpoint)
		}
	}
	return nil
}

// validateIssuerIdentifier holds an issuer identifier to RFC 8414 §2: an
// https URL with no query and no fragment.
func validateIssuerIdentifier(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != schemeHTTPS || u.Host == "" || u.RawQuery != "" || u.Fragment != "" ||
		strings.ContainsAny(issuer, "?#") {
		return fmt.Errorf("%w: issuer %q is not an https URL without query or fragment", ErrInvalidMetadata, issuer)
	}
	return nil
}

// MetadataURL is where RFC 8414 §3.1 puts issuer's metadata: the well-known
// suffix between the host and any path, so issuers sharing a host do not
// share a document.
func MetadataURL(issuer string) (string, error) {
	if err := validateIssuerIdentifier(issuer); err != nil {
		return "", err
	}
	u, _ := url.Parse(issuer) // validated above
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	return u.Scheme + "://" + u.Host + wellKnownMetadata + path, nil
}

// MetadataHandler serves m as RFC 8414 §3.2 has it served: JSON, 200.
func MetadataHandler(m IssuerMetadata) (http.Handler, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	doc, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("limes: marshal metadata: %w", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", mediaTypeJSON)
		w.Header().Set(headerCacheControl, fmt.Sprintf("public, %s=%d", directiveMaxAge, int64(metadataMaxAge/time.Second)))
		_, _ = w.Write(doc)
	}), nil
}

// mediaTypeJSON is application/json, the media type of metadata (§3.2).
const mediaTypeJSON = "application/json"

// DiscoverIssuer fetches issuer's metadata from its well-known URI and checks
// it is issuer's (RFC 8414 §3.3: the issuer member must be identical to the
// identifier the metadata was fetched for, or the document is not used).
// client nil uses one with a timeout.
func DiscoverIssuer(ctx context.Context, client *http.Client, issuer string) (IssuerMetadata, error) {
	where, err := MetadataURL(issuer)
	if err != nil {
		return IssuerMetadata{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: defaultMetadataFetchTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, where, nil)
	if err != nil {
		return IssuerMetadata{}, err
	}
	req.Header.Set("Accept", mediaTypeJSON)
	resp, err := client.Do(req)
	if err != nil {
		return IssuerMetadata{}, fmt.Errorf("limes: fetch metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return IssuerMetadata{}, fmt.Errorf("%w: %s answered HTTP %d", ErrInvalidMetadata, where, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil {
		return IssuerMetadata{}, fmt.Errorf("limes: read metadata: %w", err)
	}
	if len(body) > maxMetadataBytes {
		return IssuerMetadata{}, fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidMetadata, maxMetadataBytes)
	}
	var m IssuerMetadata
	// Unknown members ignored, as §3.2 has them; names in their exact case
	// and none twice, as JSON defines them.
	if err := json.Unmarshal(body, &m); err != nil {
		return IssuerMetadata{}, fmt.Errorf("%w: %w", ErrInvalidMetadata, err)
	}
	if m.Issuer != issuer {
		return IssuerMetadata{}, fmt.Errorf("%w: document names issuer %q, fetched for %q", ErrInvalidMetadata, m.Issuer, issuer)
	}
	if err := m.Validate(); err != nil {
		return IssuerMetadata{}, err
	}
	return m, nil
}
