package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/oleg-tkachuk/limes"
)

// The introspection request's parameters (RFC 7662 §2.1).
const (
	paramToken         = "token"
	paramTokenTypeHint = "token_type_hint"
)

// AuthorizationDetailsTypeCaveats is the RFC 9396 type of the
// authorization_details entry an introspection answer carries: an active
// capability's caveats, its members those of the token's limes_caveats claim.
const AuthorizationDetailsTypeCaveats = "https://github.com/oleg-tkachuk/limes/exchange#caveats"

// detailType is the member naming an authorization_details entry's type
// (RFC 9396 §2).
const detailType = "type"

// IntrospectionConfig configures an IntrospectionHandler.
type IntrospectionConfig struct {
	// Verifier introspects the tokens (limes.Verifier.Introspect). Required.
	Verifier *limes.Verifier
	// Authenticate authenticates the resource server or gateway asking, as
	// RFC 7662 §2.1 requires, so the endpoint is no oracle for whoever holds
	// a token. An error refuses the request as ErrInvalidClient. Required.
	Authenticate func(r *http.Request) (Client, error)
	// Audiences are the audiences of the tokens a client may learn about
	// (RFC 7662 §4): a token whose aud names none of them is answered
	// inactive, so one resource server learns nothing of tokens meant for
	// another. Nil: any authenticated client learns about any token.
	Audiences func(Client) []string
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
// sub and act as the token names them (RFC 8693 §4.1), cnf when it is bound
// (RFC 9449 §6.2), client_id when it names one, and what it allows as one
// authorization_details entry (RFC 9396 §9.2).
type introspection struct {
	Active    bool     `json:"active"`
	Issuer    string   `json:"iss,omitempty"`
	Subject   string   `json:"sub,omitempty"`
	Audience  []string `json:"aud,omitempty"`
	ExpiresAt int64    `json:"exp,omitempty"`
	IssuedAt  int64    `json:"iat,omitempty"`
	NotBefore int64    `json:"nbf,omitempty"`
	ID        string   `json:"jti,omitempty"`
	// ClientID is the client the capability's grant began with, which a
	// delegated child keeps, not the client presenting it; sub and act name
	// the holders.
	ClientID  string `json:"client_id,omitempty"`
	TokenType string `json:"token_type,omitempty"`
	Cnf       *cnf   `json:"cnf,omitempty"`
	Act       *act   `json:"act,omitempty"`
	// AuthorizationDetails holds one entry, of AuthorizationDetailsTypeCaveats.
	AuthorizationDetails []json.RawMessage `json:"authorization_details,omitempty"`
}

// act is RFC 8693 §4.1's actor: the party acting, and nested within it the
// party that acted before it.
type act struct {
	Subject string `json:"sub"`
	Act     *act   `json:"act,omitempty"`
}

// actingOf is the sub and act of an answer about c: the principal it acts
// on behalf of, acted for by its holder and the holders it was delegated
// through, nearest first; or its holder alone, acting for itself.
func actingOf(c limes.Capability) (string, *act) {
	if c.OnBehalfOf == nil {
		return c.Subject.Subject, nil
	}
	actor := &act{Subject: c.Subject.Subject}
	at := actor
	for _, prior := range c.PriorActors {
		at.Act = &act{Subject: prior}
		at = at.Act
	}
	return c.OnBehalfOf.Subject, actor
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
	case errors.Is(err, limes.ErrCopyCountersNotMetered):
		// A copy whose limits this verifier does not count is no token it
		// vouches for, as Verify refuses it; answering it active would state
		// the capability's wider limits as the copy's.
		log.ErrorContext(r.Context(), "introspection: a copy's limits need a verifier with MeterCopies; answered inactive", slog.Any("reason", err))
		writeJSON(w, http.StatusOK, introspection{Active: false}, log)
	case err != nil:
		// An outage must not read as a token gone inactive: the gateway
		// would refuse every request as if every token were revoked.
		log.ErrorContext(r.Context(), "introspection failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: codeServerError}, log)
	case !h.mayAsk(client, got.Capability.Audience):
		log.WarnContext(r.Context(), "introspection: token for an audience the client may not ask about; answered inactive",
			slog.String("capability", got.Capability.ID.String()))
		writeJSON(w, http.StatusOK, introspection{Active: false}, log)
	default:
		h.answerActive(r.Context(), w, log, got)
	}
}

// mayAsk reports whether client may learn about a token of audience: any
// client, when Audiences is nil, or one serving an audience the token names.
func (h *IntrospectionHandler) mayAsk(client Client, audience []string) bool {
	if h.cfg.Audiences == nil {
		return true
	}
	served := h.cfg.Audiences(client)
	return slices.ContainsFunc(audience, func(aud string) bool { return slices.Contains(served, aud) })
}

// answerActive writes the answer for an active token: its standard
// members, who it acts for, its binding, and its caveats.
func (h *IntrospectionHandler) answerActive(ctx context.Context, w http.ResponseWriter, log *slog.Logger, got limes.Introspection) {
	c := got.Capability
	detail, err := caveatsDetail(effectiveCaveats(c.Caveats, got.Copies))
	if err != nil {
		log.ErrorContext(ctx, "introspection: encode caveats", slog.String("capability", c.ID.String()), slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: codeServerError}, log)
		return
	}
	sub, actor := actingOf(c)
	answer := introspection{
		Active: true, Issuer: c.Issuer, Subject: sub, Act: actor, Audience: c.Audience,
		ExpiresAt: c.ExpiresAt.Unix(), IssuedAt: c.IssuedAt.Unix(), ID: c.ID.String(), ClientID: c.ClientID,
		TokenType: TokenTypeBearer, AuthorizationDetails: []json.RawMessage{detail},
	}
	if !c.NotBefore.IsZero() {
		answer.NotBefore = c.NotBefore.Unix()
	}
	if c.ConfirmationJKT != "" {
		answer.TokenType, answer.Cnf = TokenTypeDPoP, &cnf{JKT: c.ConfirmationJKT}
	}
	log.InfoContext(ctx, "introspection: token active",
		slog.String("capability", answer.ID), slog.String("token_type", answer.TokenType))
	writeJSON(w, http.StatusOK, answer, log)
}

// effectiveCaveats are c with each request and budget ceiling lowered to the
// tightest a copy's blocks set, so an answer about a Biscuit copy states no
// more than the copy may spend. A copy's limit only narrows: it is never
// Unlimited, and one above the capability's leaves the capability's.
func effectiveCaveats(c limes.Caveats, copies []limes.CopyCeiling) limes.Caveats {
	for _, cc := range copies {
		if r := cc.Requests; r != nil && (c.Requests.Unlimited || r.Max < c.Requests.Max) {
			c.Requests = limes.RequestCeiling{Max: r.Max}
		}
		if b := cc.Budget; b != nil && (c.Budget.Unlimited || b.Max < c.Budget.Max) {
			c.Budget.Max, c.Budget.Unlimited = b.Max, false
		}
	}
	return c
}

// caveatsDetail is c as an authorization_details entry: the members of the
// limes_caveats claim, under AuthorizationDetailsTypeCaveats.
func caveatsDetail(c limes.Caveats) (json.RawMessage, error) {
	raw, err := limes.MarshalCaveats(c)
	if err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	typ, err := json.Marshal(AuthorizationDetailsTypeCaveats)
	if err != nil {
		return nil, err
	}
	members[detailType] = typ
	return json.Marshal(members)
}

// readRequest reads and authenticates an introspection request: its token,
// and who asks. token_type_hint is read and ignored (§2.1).
func (h *IntrospectionHandler) readRequest(w http.ResponseWriter, r *http.Request) (string, Client, error) {
	if _, err := readForm(w, r, h.cfg.MaxBodyBytes); err != nil {
		return "", Client{}, err
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
