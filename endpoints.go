package limes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// The token revocation endpoint of RFC 7009 and the token introspection
// endpoint of RFC 7662, over a StandardVerifier and a Store. Both take a
// form-encoded POST with a `token` parameter from an authenticated client;
// who the clients are, and what each may revoke or see, is the consumer's,
// through the callbacks below.

// Form and response members of RFC 7009 §2.1, RFC 7662 §2.1–2.2 and RFC
// 6749 §5.2.
const (
	paramToken        = "token"
	mediaTypeForm     = "application/x-www-form-urlencoded"
	codeInvalidClient = "invalid_client"
	// tokenTypeBearer and tokenTypeDPoP are introspection's token_type
	// (RFC 7662 §2.2, RFC 9449 §6.2): the scheme the token is presented
	// under.
	tokenTypeBearer = string(AuthSchemeBearer)
	tokenTypeDPoP   = string(AuthSchemeDPoP)
	// maxFormBytes caps a request body: one token and a hint.
	maxFormBytes = 64 << 10
	// revocationReason is the Reason a revocation through the endpoint
	// records.
	revocationReason = "rfc7009"
)

// ErrClientUnauthorized is what a ClientAuthenticator returns for a client
// it does not recognise; the endpoints answer it with invalid_client.
var ErrClientUnauthorized = errors.New("limes: client not authenticated")

// ClientAuthenticator authenticates the client calling an endpoint (RFC
// 7009 §2.1, RFC 7662 §2.1), from its credentials on r: a client secret, a
// certificate, a bearer token of its own. It returns the client, or
// ErrClientUnauthorized.
type ClientAuthenticator func(r *http.Request) (Principal, error)

// ClientAuthorizer reports whether client may act on capability c: revoke
// it, or see it introspected. RFC 7009 §2.1 lets a client revoke only the
// tokens issued to it; RFC 7662 §4 lets a resource server see only the
// tokens meant for it.
type ClientAuthorizer func(client Principal, c Capability) bool

// RevocationConfig wires RevocationHandler.
type RevocationConfig struct {
	Verifier     *StandardVerifier
	Store        Store
	Authenticate ClientAuthenticator
	Authorize    ClientAuthorizer
	// Biscuits revokes a Biscuit presented to the endpoint as that copy
	// alone. Without it, a copy is revoked with its whole capability.
	Biscuits BiscuitRevocationStore
}

// RevocationHandler is an RFC 7009 token revocation endpoint. It revokes the
// token in the `token` parameter — a capability, or one copy of its Biscuit —
// and answers 200 whether or not the token was valid (§2.2): a client
// learns nothing about a token it holds by revoking it. A token the client
// may not revoke is answered the same way and left alone.
func RevocationHandler(cfg RevocationConfig) (http.Handler, error) {
	if cfg.Verifier == nil || cfg.Store == nil || cfg.Authenticate == nil || cfg.Authorize == nil {
		return nil, errors.New("limes: RevocationConfig needs Verifier, Store, Authenticate and Authorize")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, token, ok := endpointRequest(w, r, cfg.Authenticate)
		if !ok {
			return
		}
		c, biscuitIDs, err := cfg.Verifier.identify(r.Context(), token)
		if err != nil || !cfg.Authorize(client, *c) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := revoke(r.Context(), cfg, client, c, biscuitIDs); err != nil {
			// §2.2.1: the server is temporarily unable to revoke.
			writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
			return
		}
		w.WriteHeader(http.StatusOK)
	}), nil
}

func revoke(ctx context.Context, cfg RevocationConfig, client Principal, c *Capability, biscuitIDs [][]byte) error {
	if biscuitIDs != nil && cfg.Biscuits != nil {
		return cfg.Biscuits.RevokeBiscuit(ctx, RevokeBiscuitRequest{
			CapabilityID: c.ID, RevocationID: biscuitIDs[len(biscuitIDs)-1],
			Reason: revocationReason, Actor: client.Subject,
		})
	}
	err := cfg.Store.Revoke(ctx, RevokeRequest{ID: c.ID, Reason: revocationReason, Actor: client.Subject})
	if errors.Is(err, ErrNotFound) {
		return nil // nothing on record to revoke: as invalid a token as any
	}
	return err
}

// IntrospectionConfig wires IntrospectionHandler.
type IntrospectionConfig struct {
	Verifier     *StandardVerifier
	Authenticate ClientAuthenticator
	Authorize    ClientAuthorizer
}

// Introspection is an RFC 7662 §2.2 response.
type Introspection struct {
	Active bool `json:"active"`
	// Every member below is present only when Active is.
	TokenType            string                `json:"token_type,omitempty"`
	Issuer               string                `json:"iss,omitempty"`
	Subject              string                `json:"sub,omitempty"`
	Audience             []string              `json:"aud,omitempty"`
	IssuedAt             int64                 `json:"iat,omitempty"`
	NotBefore            int64                 `json:"nbf,omitempty"`
	ExpiresAt            int64                 `json:"exp,omitempty"`
	ID                   string                `json:"jti,omitempty"`
	Actor                *Actor                `json:"act,omitempty"`
	Confirmation         *cnfClaim             `json:"cnf,omitempty"`
	AuthorizationDetails []AuthorizationDetail `json:"authorization_details,omitempty"`
}

// NewIntrospection is c as an active introspection response: the claims of
// its token, its binding (RFC 9449 §6.2) and its authorization details (RFC
// 9396 §9.2).
func NewIntrospection(c Capability) Introspection {
	in := Introspection{
		Active: true, TokenType: tokenTypeBearer,
		Issuer: c.Issuer, Subject: c.Authority(), Audience: c.Audience,
		IssuedAt: c.IssuedAt.Unix(), ExpiresAt: c.ExpiresAt.Unix(), ID: c.ID.String(),
		Actor:                c.Actor,
		AuthorizationDetails: []AuthorizationDetail{NewAuthorizationDetail(c.Caveats)},
	}
	if !c.NotBefore.IsZero() {
		in.NotBefore = c.NotBefore.Unix()
	}
	if c.ConfirmationJKT != "" {
		in.TokenType, in.Confirmation = tokenTypeDPoP, &cnfClaim{JKT: c.ConfirmationJKT}
	}
	return in
}

// IntrospectionHandler is an RFC 7662 token introspection endpoint. It
// answers {"active": false} for a token that is invalid, expired, revoked,
// or one the client may not see (§2.2, §4), and the token's claims for any
// other. The audience and the proof of possession are the calling resource
// server's to check.
func IntrospectionHandler(cfg IntrospectionConfig) (http.Handler, error) {
	if cfg.Verifier == nil || cfg.Authenticate == nil || cfg.Authorize == nil {
		return nil, errors.New("limes: IntrospectionConfig needs Verifier, Authenticate and Authorize")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, token, ok := endpointRequest(w, r, cfg.Authenticate)
		if !ok {
			return
		}
		answer := Introspection{}
		if c, err := cfg.Verifier.Introspect(r.Context(), token); err == nil && cfg.Authorize(client, c) {
			answer = NewIntrospection(c)
		}
		writeJSON(w, http.StatusOK, answer)
	}), nil
}

// endpointRequest reads an endpoint's request: a POST of a form with a
// token, from a client Authenticate accepts. On anything else it writes the
// error response and reports false.
func endpointRequest(w http.ResponseWriter, r *http.Request, authenticate ClientAuthenticator) (Principal, string, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeOAuthError(w, http.StatusMethodNotAllowed, codeInvalidRequest)
		return Principal{}, "", false
	}
	mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if !strings.EqualFold(strings.TrimSpace(mediaType), mediaTypeForm) {
		writeOAuthError(w, http.StatusUnsupportedMediaType, codeInvalidRequest)
		return Principal{}, "", false
	}
	client, err := authenticate(r)
	if err != nil {
		// RFC 6749 §5.2: invalid_client, 401.
		writeOAuthError(w, http.StatusUnauthorized, codeInvalidClient)
		return Principal{}, "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil || r.PostForm.Get(paramToken) == "" {
		writeOAuthError(w, http.StatusBadRequest, codeInvalidRequest)
		return Principal{}, "", false
	}
	return client, r.PostForm.Get(paramToken), true
}

// writeOAuthError writes an RFC 6749 §5.2 error response.
func writeOAuthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mediaTypeJSON)
	// RFC 7662 §2.2 and RFC 6749 §5.1: a token's status is not cached.
	w.Header().Set(headerCacheControl, directiveNoStore)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
