package exchange

import (
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"

	"github.com/oleg-tkachuk/limes"
)

// The introspection request's parameters (RFC 7662 §2.1).
const (
	paramToken         = "token"
	paramTokenTypeHint = "token_type_hint"
)

// IntrospectionConfig configures an IntrospectionHandler.
type IntrospectionConfig struct {
	// Verifier introspects the tokens (limes.Verifier.Introspect). Required.
	Verifier *limes.Verifier
	// Authenticate authenticates the resource server or gateway asking, as
	// RFC 7662 §2.1 requires, so the endpoint is no oracle for whoever holds
	// a token. An error refuses the request as ErrInvalidClient. Required.
	Authenticate func(r *http.Request) (Client, error)
	// MaxBodyBytes bounds the request body. Zero: DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// Logger logs every answer: active at INFO, inactive at WARN with the
	// reason, the server's failures at ERROR. Nil: slog.Default().
	Logger *slog.Logger
}

// IntrospectionHandler is an RFC 7662 token introspection endpoint, for a
// gateway that does not read capability tokens itself: it answers whether a
// token is active, and what it is.
type IntrospectionHandler struct {
	cfg IntrospectionConfig
}

// NewIntrospectionHandler builds an IntrospectionHandler, refusing a config
// without a verifier or a client authenticator.
func NewIntrospectionHandler(cfg IntrospectionConfig) (*IntrospectionHandler, error) {
	switch {
	case cfg.Verifier == nil:
		return nil, errors.New("exchange: IntrospectionConfig.Verifier required")
	case cfg.Authenticate == nil:
		return nil, errors.New("exchange: IntrospectionConfig.Authenticate required")
	case cfg.MaxBodyBytes < 0:
		return nil, errors.New("exchange: IntrospectionConfig.MaxBodyBytes is negative")
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &IntrospectionHandler{cfg: cfg}, nil
}

// introspection is an answer (RFC 7662 §2.2): active alone for a token that
// is not, so nothing about it leaks; for one that is, its standard members,
// cnf when it is bound (RFC 9449 §6.2), and client_id when it names one.
// What the capability allows, its caveats, is for a verifier of limes
// tokens to read.
type introspection struct {
	Active    bool     `json:"active"`
	Issuer    string   `json:"iss,omitempty"`
	Subject   string   `json:"sub,omitempty"`
	Audience  []string `json:"aud,omitempty"`
	ExpiresAt int64    `json:"exp,omitempty"`
	IssuedAt  int64    `json:"iat,omitempty"`
	NotBefore int64    `json:"nbf,omitempty"`
	ID        string   `json:"jti,omitempty"`
	ClientID  string   `json:"client_id,omitempty"`
	TokenType string   `json:"token_type,omitempty"`
	Cnf       *cnf     `json:"cnf,omitempty"`
}

// cnf is a bound token's confirmation (RFC 7800, RFC 9449 §6.2).
type cnf struct {
	JKT string `json:"jkt"`
}

// ServeHTTP answers an introspection request.
func (h *IntrospectionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	log := h.cfg.Logger
	token, client, err := h.readRequest(w, r)
	if err != nil {
		log.WarnContext(r.Context(), "introspection request refused", slog.Any("error", err))
		h.refuse(w, err)
		return
	}
	log = log.With(slog.String("client", client.ID))
	got, err := h.cfg.Verifier.Introspect(r.Context(), token)
	switch {
	case errors.Is(err, limes.ErrUnauthenticated):
		log.WarnContext(r.Context(), "introspection: token inactive", slog.Any("reason", err))
		writeJSON(w, http.StatusOK, introspection{Active: false}, log)
	case err != nil:
		// An outage must not read as a token gone inactive: the gateway
		// would refuse every request as if every token were revoked.
		log.ErrorContext(r.Context(), "introspection failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: codeServerError}, log)
	default:
		c := got.Capability
		answer := introspection{
			Active: true, Issuer: c.Issuer, Subject: c.Subject.Subject, Audience: c.Audience,
			ExpiresAt: c.ExpiresAt.Unix(), IssuedAt: c.IssuedAt.Unix(), ID: c.ID.String(), ClientID: c.ClientID,
			TokenType: TokenTypeBearer,
		}
		if !c.NotBefore.IsZero() {
			answer.NotBefore = c.NotBefore.Unix()
		}
		if c.ConfirmationJKT != "" {
			answer.TokenType, answer.Cnf = TokenTypeDPoP, &cnf{JKT: c.ConfirmationJKT}
		}
		log.InfoContext(r.Context(), "introspection: token active",
			slog.String("capability", answer.ID), slog.String("token_type", answer.TokenType))
		writeJSON(w, http.StatusOK, answer, log)
	}
}

// readRequest reads and authenticates an introspection request: its token,
// and who asks. token_type_hint is read and ignored (§2.1).
func (h *IntrospectionHandler) readRequest(w http.ResponseWriter, r *http.Request) (string, Client, error) {
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != formContentType {
		return "", Client{}, fmt.Errorf("%w: the body must be %s", ErrInvalidRequest, formContentType)
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes)
	if err := r.ParseForm(); err != nil {
		return "", Client{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	for _, name := range []string{paramToken, paramTokenTypeHint} {
		if len(r.PostForm[name]) > 1 {
			return "", Client{}, fmt.Errorf("%w: %s sent %d times", ErrInvalidRequest, name, len(r.PostForm[name]))
		}
	}
	token := r.PostForm.Get(paramToken)
	if token == "" {
		return "", Client{}, fmt.Errorf("%w: %s required", ErrInvalidRequest, paramToken)
	}
	client, err := h.cfg.Authenticate(r)
	if err != nil {
		return "", Client{}, fmt.Errorf("%w: %w", ErrInvalidClient, err)
	}
	return token, client, nil
}

// refuse answers a request that could not be read or authenticated.
func (h *IntrospectionHandler) refuse(w http.ResponseWriter, err error) {
	status, code, description := http.StatusBadRequest, codeInvalidRequest, "The request is malformed."
	if errors.Is(err, ErrInvalidClient) {
		status, code, description = http.StatusUnauthorized, codeInvalidClient, "The client could not be authenticated."
	}
	writeJSON(w, status, errorResponse{Error: code, Description: description}, h.cfg.Logger)
}
