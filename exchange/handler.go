package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oleg-tkachuk/limes/dpop"
)

// The token request's parameters and values (RFC 8693 §2.1, RFC 8707 §2,
// RFC 9396 §2, RFC 6749 §3.3).
const (
	GrantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange" //nolint:gosec // G101: a grant type URI, not a credential
	// The subject token types a JWT access token arrives as.
	TokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token" //nolint:gosec // G101: a token type URI, not a credential
	TokenTypeJWT         = "urn:ietf:params:oauth:token-type:jwt"          //nolint:gosec // G101: a token type URI, not a credential

	paramGrantType            = "grant_type"
	paramSubjectToken         = "subject_token"
	paramSubjectTokenType     = "subject_token_type"
	paramActorToken           = "actor_token"
	paramActorTokenType       = "actor_token_type"
	paramRequestedTokenType   = "requested_token_type"
	paramResource             = "resource"
	paramAudience             = "audience"
	paramScope                = "scope"
	paramAuthorizationDetails = "authorization_details"

	// formContentType is the token request's media type (RFC 6749 §3.2).
	formContentType = "application/x-www-form-urlencoded"
	jsonContentType = "application/json"
	headerDPoP      = "DPoP"
	headerDPoPNonce = "DPoP-Nonce"
	// cacheNoStore keeps a token response out of every cache (RFC 6749 §5.1).
	cacheNoStore = "no-store"
)

// repeatable are the parameters a request may send more than once (RFC
// 8693 §2.1, RFC 8707 §2); RFC 6749 §3.2 forbids repeating any other.
var repeatable = map[string]bool{paramResource: true, paramAudience: true}

// DefaultMaxBodyBytes bounds a token request's body when the config names
// no MaxBodyBytes: a subject token, a proof and the parameters fit in a few
// kilobytes.
const DefaultMaxBodyBytes = 64 << 10

// The RFC 6749 §5.2 error codes, with RFC 8707's and RFC 9449's.
const (
	codeInvalidRequest       = "invalid_request"
	codeInvalidClient        = "invalid_client"
	codeUnauthorizedClient   = "unauthorized_client"
	codeInvalidGrant         = "invalid_grant"
	codeInvalidTarget        = "invalid_target"
	codeInvalidScope         = "invalid_scope"
	codeUnsupportedGrantType = "unsupported_grant_type"
	codeInvalidDPoPProof     = "invalid_dpop_proof"
	codeUseDPoPNonce         = "use_dpop_nonce"
	codeServerError          = "server_error"
)

// HandlerConfig configures a Handler.
type HandlerConfig struct {
	// Exchanger exchanges the tokens. Required.
	Exchanger *Exchanger
	// Authenticate authenticates the client making the request — by mTLS,
	// a client assertion, a shared secret — and names the audiences its
	// subject tokens may be issued for. An error that is or wraps
	// ErrUnauthorizedClient refuses an authenticated client that may not
	// exchange: 400 unauthorized_client. Any other error refuses the client
	// as not authenticated, ErrInvalidClient: 401 invalid_client. Required.
	Authenticate func(r *http.Request) (Client, error)
	// EndpointURL is the token endpoint's URL as clients call it, which
	// their DPoP proofs name (htu). Required when Exchanger checks DPoP.
	EndpointURL string
	// MaxBodyBytes bounds the request body. Zero: DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// Logger logs refusals of malformed requests; the Exchanger logs the
	// rest. Nil: slog.Default().
	Logger *slog.Logger
}

// Handler is an RFC 8693 token endpoint over an Exchanger, for a gateway or
// a token service shared within one trust domain.
type Handler struct {
	cfg HandlerConfig
}

// NewHandler builds a Handler, refusing a config without an exchanger or a
// client authenticator, or one that checks DPoP with no endpoint URL.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	switch {
	case cfg.Exchanger == nil:
		return nil, errors.New("exchange: HandlerConfig.Exchanger required")
	case cfg.Authenticate == nil:
		return nil, errors.New("exchange: HandlerConfig.Authenticate required")
	case (cfg.Exchanger.cfg.DPoP != nil || cfg.Exchanger.cfg.Capabilities != nil) && cfg.EndpointURL == "":
		// A bound capability is narrowed with a proof over it, which its
		// verifier checks against this URL.
		return nil, errors.New("exchange: HandlerConfig.EndpointURL required to check DPoP proofs")
	case cfg.MaxBodyBytes < 0:
		return nil, errors.New("exchange: HandlerConfig.MaxBodyBytes is negative")
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Handler{cfg: cfg}, nil
}

// tokenResponse is a successful response (RFC 8693 §2.2.1).
type tokenResponse struct {
	AccessToken     string `json:"access_token"`
	IssuedTokenType string `json:"issued_token_type"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int64  `json:"expires_in"`
}

// errorResponse is a refusal (RFC 6749 §5.2). The description is fixed per
// code: an error's own text, which can name subjects and clients, stays in
// the log.
type errorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

// ServeHTTP answers a token exchange request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	req, subjectType, err := h.readRequest(w, r)
	if err != nil {
		h.cfg.Logger.WarnContext(r.Context(), "exchange request refused", slog.Any("error", err))
		h.refuse(w, r, err)
		return
	}
	var res Result
	if subjectType == IssuedTokenTypeCapability {
		res, err = h.cfg.Exchanger.Narrow(r.Context(), NarrowRequest(req))
	} else {
		res, err = h.cfg.Exchanger.Exchange(r.Context(), req)
	}
	if err != nil {
		h.refuse(w, r, err)
		return
	}
	expiresIn := int64(res.Capability.ExpiresAt.Sub(h.cfg.Exchanger.cfg.Now()).Round(time.Second) / time.Second)
	h.write(w, http.StatusOK, tokenResponse{
		AccessToken: res.Token, IssuedTokenType: IssuedTokenTypeCapability, TokenType: res.TokenType, ExpiresIn: expiresIn,
	})
}

// readRequest reads and authenticates a token exchange request, and names
// the type of its subject token.
func (h *Handler) readRequest(w http.ResponseWriter, r *http.Request) (Request, string, error) {
	form, err := readForm(w, r, h.cfg.MaxBodyBytes)
	if err != nil {
		return Request{}, "", err
	}
	if err := checkForm(form); err != nil {
		return Request{}, "", err
	}
	client, err := h.cfg.Authenticate(r)
	switch {
	case errors.Is(err, ErrUnauthorizedClient):
		return Request{}, "", err
	case err != nil:
		return Request{}, "", fmt.Errorf("%w: %w", ErrInvalidClient, err)
	}
	proof, err := h.proofOf(r)
	if err != nil {
		return Request{}, "", err
	}
	req := Request{
		Client: client, SubjectToken: form.Get(paramSubjectToken),
		Resources: form[paramResource], Audiences: form[paramAudience], Scopes: strings.Fields(form.Get(paramScope)),
		DPoP: proof,
	}
	if details := form.Get(paramAuthorizationDetails); details != "" {
		req.AuthorizationDetails = json.RawMessage(details)
	}
	return req, form.Get(paramSubjectTokenType), nil
}

// readForm reads a request's form body, no larger than maxBytes, refusing
// any other body.
func readForm(w http.ResponseWriter, r *http.Request, maxBytes int64) (url.Values, error) {
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != formContentType {
		return nil, fmt.Errorf("%w: the body must be %s", ErrInvalidRequest, formContentType)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return r.PostForm, nil
}

// proofOf is the request's DPoP proof, the zero Proof for none; more than
// one is refused.
func (h *Handler) proofOf(r *http.Request) (dpop.Proof, error) {
	switch proofs := r.Header.Values(headerDPoP); len(proofs) {
	case 0:
		return dpop.Proof{}, nil
	case 1:
		return dpop.Proof{JWT: proofs[0], Method: r.Method, URL: h.cfg.EndpointURL}, nil
	default:
		return dpop.Proof{}, fmt.Errorf("%w: %d DPoP headers", dpop.ErrInvalid, len(proofs))
	}
}

// checkForm checks a token exchange request's parameters, all but who sent
// it: the grant, the subject token and its type, the targets, and what this
// endpoint does not support.
func checkForm(form url.Values) error {
	for _, check := range []func(url.Values) error{
		checkRepeats, checkGrantType, checkTokenTypes, checkTargets, checkDetails,
	} {
		if err := check(form); err != nil {
			return err
		}
	}
	return nil
}

// checkRepeats refuses a parameter sent more than once that may not be.
func checkRepeats(form url.Values) error {
	for name, values := range form {
		if len(values) > 1 && !repeatable[name] {
			return fmt.Errorf("%w: %s sent %d times", ErrInvalidRequest, name, len(values))
		}
	}
	return nil
}

// checkGrantType refuses a grant other than token exchange, or one without
// a subject token.
func checkGrantType(form url.Values) error {
	if grant := form.Get(paramGrantType); grant != GrantTypeTokenExchange {
		if grant == "" {
			return fmt.Errorf("%w: %s required", ErrInvalidRequest, paramGrantType)
		}
		return fmt.Errorf("%w: %q", ErrUnsupportedGrantType, grant)
	}
	if form.Get(paramSubjectToken) == "" {
		return fmt.Errorf("%w: %s required", ErrInvalidRequest, paramSubjectToken)
	}
	return nil
}

// checkTokenTypes refuses a subject token of a type this endpoint does not
// exchange, an actor token, or a requested type it does not issue.
func checkTokenTypes(form url.Values) error {
	switch typ := form.Get(paramSubjectTokenType); typ {
	case TokenTypeAccessToken, TokenTypeJWT, IssuedTokenTypeCapability:
	default:
		return fmt.Errorf("%w: %s %q is not one this endpoint exchanges", ErrInvalidRequest, paramSubjectTokenType, typ)
	}
	if form.Has(paramActorToken) || form.Has(paramActorTokenType) {
		return fmt.Errorf("%w: %s is not supported", ErrInvalidRequest, paramActorToken)
	}
	if typ := form.Get(paramRequestedTokenType); typ != "" && typ != IssuedTokenTypeCapability {
		return fmt.Errorf("%w: %s %q is not the capability this endpoint issues", ErrInvalidRequest, paramRequestedTokenType, typ)
	}
	return nil
}

// checkTargets refuses a resource that is no absolute URI without a
// fragment (RFC 8707 §2), or an empty audience.
func checkTargets(form url.Values) error {
	for _, resource := range form[paramResource] {
		u, err := url.Parse(resource)
		if err != nil || !u.IsAbs() || u.Fragment != "" || strings.Contains(resource, "#") {
			return fmt.Errorf("%w: %s %q is not an absolute URI without a fragment", ErrInvalidTarget, paramResource, resource)
		}
	}
	for _, audience := range form[paramAudience] {
		if audience == "" {
			return fmt.Errorf("%w: an empty %s", ErrInvalidTarget, paramAudience)
		}
	}
	return nil
}

// checkDetails refuses authorization_details that is not a JSON array.
func checkDetails(form url.Values) error {
	details := form.Get(paramAuthorizationDetails)
	if details == "" {
		return nil
	}
	var array []json.RawMessage
	if err := json.Unmarshal([]byte(details), &array); err != nil {
		return fmt.Errorf("%w: %s is not a JSON array", ErrInvalidRequest, paramAuthorizationDetails)
	}
	return nil
}

// refusals is each refusal's status, code and description, the first
// matching rule answering.
var refusals = []struct {
	err         error
	status      int
	code        string
	description string
}{
	{dpop.ErrNonceRequired, http.StatusBadRequest, codeUseDPoPNonce, "The DPoP proof must carry the server's nonce."},
	{dpop.ErrRequired, http.StatusBadRequest, codeInvalidDPoPProof, "A DPoP proof is required."},
	{dpop.ErrInvalid, http.StatusBadRequest, codeInvalidDPoPProof, "The DPoP proof is invalid."},
	{dpop.ErrReplayed, http.StatusBadRequest, codeInvalidDPoPProof, "The DPoP proof was used before."},
	{ErrUnauthorizedClient, http.StatusBadRequest, codeUnauthorizedClient, "The client may not exchange tokens."},
	{ErrInvalidClient, http.StatusUnauthorized, codeInvalidClient, "The client could not be authenticated."},
	{ErrUnsupportedGrantType, http.StatusBadRequest, codeUnsupportedGrantType, "Only token exchange is supported."},
	{ErrInvalidGrant, http.StatusBadRequest, codeInvalidGrant, "The subject token was refused."},
	{ErrInvalidTarget, http.StatusBadRequest, codeInvalidTarget, "The resource or audience was refused."},
	{ErrInvalidScope, http.StatusBadRequest, codeInvalidScope, "The scope was refused."},
	{ErrInvalidRequest, http.StatusBadRequest, codeInvalidRequest, "The request is malformed."},
}

// refuse answers err.
func (h *Handler) refuse(w http.ResponseWriter, r *http.Request, err error) {
	for _, rule := range refusals {
		if !errors.Is(err, rule.err) {
			continue
		}
		if rule.code == codeUseDPoPNonce {
			nonce, nerr := h.nonce(r.Context())
			if nerr != nil {
				h.cfg.Logger.ErrorContext(r.Context(), "exchange: DPoP nonce", slog.Any("error", nerr))
				break
			}
			if nonce == "" {
				h.cfg.Logger.ErrorContext(r.Context(), "exchange: a nonce is required, and no verifier issues one; give Config.Capabilities and Config.DPoP the same nonces")
			} else {
				w.Header().Set(headerDPoPNonce, nonce)
			}
		}
		h.write(w, rule.status, errorResponse{Error: rule.code, Description: rule.description})
		return
	}
	h.write(w, http.StatusInternalServerError, errorResponse{Error: codeServerError})
}

// nonce is the DPoP nonce a use_dpop_nonce answer carries: Config.DPoP's,
// or, with none, that of Config.Capabilities' verifier, whose proof check
// asked for it. It is empty when neither issues nonces.
func (h *Handler) nonce(ctx context.Context) (string, error) {
	cfg := h.cfg.Exchanger.cfg
	switch {
	case cfg.DPoP != nil:
		return cfg.DPoP.Nonce(ctx)
	case cfg.Capabilities != nil:
		return cfg.Capabilities.DPoPNonce(ctx)
	}
	return "", nil
}

// write writes body as a token endpoint's JSON response.
func (h *Handler) write(w http.ResponseWriter, status int, body any) {
	writeJSON(w, status, body, h.cfg.Logger)
}

// writeJSON writes body as a JSON response no cache keeps, as a token
// endpoint's (RFC 6749 §5.1) and an introspection endpoint's (RFC 7662 §2.2)
// must be, logging a write that fails.
func writeJSON(w http.ResponseWriter, status int, body any, log *slog.Logger) {
	w.Header().Set("Content-Type", jsonContentType)
	w.Header().Set("Cache-Control", cacheNoStore)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Error("exchange: write response", slog.Any("error", err))
	}
}
