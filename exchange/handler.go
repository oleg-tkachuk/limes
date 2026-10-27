package exchange

import (
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
	// subject tokens may be issued for. An error refuses the request as
	// ErrInvalidClient. Required.
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
	case cfg.Exchanger.cfg.DPoP != nil && cfg.EndpointURL == "":
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
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != formContentType {
		return Request{}, "", fmt.Errorf("%w: the body must be %s", ErrInvalidRequest, formContentType)
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes)
	if err := r.ParseForm(); err != nil {
		return Request{}, "", fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	form := r.PostForm
	if err := checkForm(form); err != nil {
		return Request{}, "", err
	}
	client, err := h.cfg.Authenticate(r)
	if err != nil {
		return Request{}, "", fmt.Errorf("%w: %w", ErrInvalidClient, err)
	}
	req := Request{
		Client: client, SubjectToken: form.Get(paramSubjectToken),
		Resources: form[paramResource], Audiences: form[paramAudience], Scopes: strings.Fields(form.Get(paramScope)),
	}
	if details := form.Get(paramAuthorizationDetails); details != "" {
		req.AuthorizationDetails = json.RawMessage(details)
	}
	switch proofs := r.Header.Values(headerDPoP); len(proofs) {
	case 0:
	case 1:
		req.DPoP = dpop.Proof{JWT: proofs[0], Method: r.Method, URL: h.cfg.EndpointURL}
	default:
		return Request{}, "", fmt.Errorf("%w: %d DPoP headers", dpop.ErrInvalid, len(proofs))
	}
	return req, form.Get(paramSubjectTokenType), nil
}

// checkForm checks a token exchange request's parameters, all but who sent
// it: the grant, the subject token and its type, the targets, and what this
// endpoint does not support.
func checkForm(form url.Values) error {
	for name, values := range form {
		if len(values) > 1 && !repeatable[name] {
			return fmt.Errorf("%w: %s sent %d times", ErrInvalidRequest, name, len(values))
		}
	}
	if grant := form.Get(paramGrantType); grant != GrantTypeTokenExchange {
		if grant == "" {
			return fmt.Errorf("%w: %s required", ErrInvalidRequest, paramGrantType)
		}
		return fmt.Errorf("%w: %q", ErrUnsupportedGrantType, grant)
	}
	if form.Get(paramSubjectToken) == "" {
		return fmt.Errorf("%w: %s required", ErrInvalidRequest, paramSubjectToken)
	}
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
	for _, resource := range form[paramResource] {
		// RFC 8707 §2: an absolute URI, with no fragment.
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
	if details := form.Get(paramAuthorizationDetails); details != "" {
		var array []json.RawMessage
		if err := json.Unmarshal([]byte(details), &array); err != nil {
			return fmt.Errorf("%w: %s is not a JSON array", ErrInvalidRequest, paramAuthorizationDetails)
		}
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
			nonce, nerr := h.cfg.Exchanger.cfg.DPoP.Nonce(r.Context())
			if nerr != nil {
				h.cfg.Logger.ErrorContext(r.Context(), "exchange: DPoP nonce", slog.Any("error", nerr))
				break
			}
			w.Header().Set(headerDPoPNonce, nonce)
		}
		h.write(w, rule.status, errorResponse{Error: rule.code, Description: rule.description})
		return
	}
	h.write(w, http.StatusInternalServerError, errorResponse{Error: codeServerError})
}

// write writes body as a token endpoint's JSON response.
func (h *Handler) write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", jsonContentType)
	w.Header().Set("Cache-Control", cacheNoStore)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		h.cfg.Logger.Error("exchange: write response", slog.Any("error", err))
	}
}
