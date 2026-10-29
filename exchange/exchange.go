package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/internal/errs"
)

// The refusals of an exchange, one per RFC 6749 §5.2 error (and RFC 8707's
// invalid_target, RFC 9396's invalid_authorization_details) the token
// endpoint answers with. A Policy refuses with ErrInvalidGrant,
// ErrInvalidTarget, ErrInvalidScope or ErrInvalidAuthorizationDetails; any
// other error it returns is the server's, answered as such.
var (
	// ErrInvalidRequest is a request missing a parameter or malformed.
	ErrInvalidRequest = errors.New("exchange: invalid request")
	// ErrInvalidClient is a client the endpoint could not authenticate.
	ErrInvalidClient = errors.New("exchange: client not authenticated")
	// ErrUnauthorizedClient is a client the endpoint authenticated that may
	// not use it. HandlerConfig.Authenticate and
	// IntrospectionConfig.Authenticate return it, wrapped or not, to refuse
	// such a client.
	ErrUnauthorizedClient = errors.New("exchange: client not authorized to exchange")
	// ErrServerError is the server failing to decide on a client: its
	// client store unreachable, say. HandlerConfig.Authenticate and
	// IntrospectionConfig.Authenticate return it, wrapped or not, so that
	// the endpoint answers 500 server_error, logged at ERROR, rather than
	// tell the client its credentials are wrong (RFC 6749 §5.2).
	ErrServerError = errors.New("exchange: server error")
	// ErrInvalidGrant is a subject token refused: not genuine, expired, or
	// not one the client may present.
	ErrInvalidGrant = errors.New("exchange: subject token refused")
	// ErrInvalidTarget is a resource or audience the exchange will not
	// issue for.
	ErrInvalidTarget = errors.New("exchange: target refused")
	// ErrInvalidScope is a scope the exchange will not grant.
	ErrInvalidScope = errors.New("exchange: scope refused")
	// ErrInvalidAuthorizationDetails is authorization_details the exchange
	// will not grant: malformed, or of a type or content it does not
	// understand (RFC 9396 §5).
	ErrInvalidAuthorizationDetails = errors.New("exchange: authorization details refused")
	// ErrUnsupportedGrantType is a token request for another grant.
	ErrUnsupportedGrantType = errors.New("exchange: grant type unsupported")
)

// errPolicy is a policy that asked for more than the request allows: the
// server's fault, never the client's.
var errPolicy = fmt.Errorf("%w: exchange: the policy's issue request exceeds the exchange", ErrServerError)

// Client is the client asking for the exchange, as the token endpoint
// authenticated it, or as an in-process caller names itself.
type Client struct {
	// ID identifies the client: in logs, and as its share of the DPoP
	// replay cache. Required.
	ID string
	// SubjectAudiences are the audiences a subject token the client presents
	// may be issued for: a token is accepted only when its aud names one of
	// them, so a client presents only a token issued to itself. Required.
	SubjectAudiences []string
}

// Request is an exchange: the subject token, and what is asked for in
// exchange (RFC 8693 §2.1).
type Request struct {
	Client Client
	// SubjectToken is the token exchanged. Required.
	SubjectToken string
	// Resources are the RFC 8707 resources, and Audiences the RFC 8693
	// audiences, the capability is asked for. At least one of either; the
	// capability's audience is drawn from them.
	Resources, Audiences []string
	// Scopes are the scopes asked for.
	Scopes []string
	// AuthorizationDetails is the RFC 9396 authorization_details, a JSON
	// array as sent; nil for none. The policy reads it.
	AuthorizationDetails json.RawMessage
	// DPoP is the request's proof of possession, binding the capability to
	// its key (RFC 9449 §5); a zero Proof issues a bearer capability.
	DPoP dpop.Proof
}

// Grant is what a Policy decides on: a verified subject, its client, and
// what is asked for.
type Grant struct {
	Subject              Subject
	Client               Client
	Resources, Audiences []string
	Scopes               []string
	AuthorizationDetails json.RawMessage
}

// Policy turns a grant into the capability to issue, or refuses it with
// ErrInvalidGrant, ErrInvalidTarget, ErrInvalidScope or
// ErrInvalidAuthorizationDetails. A grant it does not refuse grants every
// scope asked, which the token endpoint answers as granted (RFC 8693
// §2.2.1); it refuses authorization_details of a type it does not
// understand (RFC 9396 §5). Its IssueRequest's
// Audience must be drawn from the grant's resources and audiences, or be
// empty to take them all; its ConfirmationJKT must be empty, the Exchanger
// setting it from the proof; its TTL is capped at the subject token's
// remaining lifetime. Nothing is granted that the policy did not choose:
// there is no default policy. A subject token may be exchanged again and
// again until it expires, each exchange issuing a capability of its own,
// with ceilings of its own, unless Config.Exchanges issues one capability
// per subject token: a policy that grants a budget per user, not per
// exchange, keys it on Grant.Subject (its Issuer and Subject). Set both ceilings explicitly:
// Caveats.Requests to a Max or Unlimited, since Issue refuses a capability
// no request can use and the exchange answers that as the server's
// failure; Caveats.Budget to what it may spend, the zero budget being none.
type Policy func(ctx context.Context, g Grant) (limes.IssueRequest, error)

// Result is an exchange's capability.
type Result struct {
	Capability limes.Capability
	Token      string
	// TokenType is how the capability is presented: TokenTypeDPoP when bound
	// to a key, else TokenTypeBearer (RFC 9449 §5, RFC 6750).
	TokenType string
	// Reissued reports that the capability is the one an earlier exchange
	// of the same subject token issued (Config.Exchanges).
	Reissued bool
}

// The token types of an exchange's response.
const (
	TokenTypeBearer = "Bearer"
	TokenTypeDPoP   = "DPoP"
	// IssuedTokenTypeCapability is the issued_token_type of a limes
	// capability (RFC 8693 §3): a URI of this module's own.
	IssuedTokenTypeCapability = "https://github.com/oleg-tkachuk/limes/exchange#capability" //nolint:gosec // G101: a token type URI, not a credential
)

// DefaultTTL is a capability's lifetime when the policy names none, still
// capped at the subject token's.
const DefaultTTL = 15 * time.Minute

// minTTL is the shortest lifetime an issued capability may have: Issue
// counts lifetimes in whole seconds.
const minTTL = time.Second

// Config configures an Exchanger.
type Config struct {
	// Issuer issues the capabilities. Required. No request to a token
	// endpoint names a tenant, so a store that scopes its rows by tenant
	// must record each capability in its own: with pgstore, give the
	// Issuer pgstore.ScopedIssuance(store).
	Issuer *limes.Issuer
	// Subjects verifies subject tokens. Required.
	Subjects SubjectVerifier
	// Policy decides what each grant issues. Required.
	Policy Policy
	// DPoP checks proofs of possession; nil refuses every request with one.
	DPoP *dpop.Verifier
	// Capabilities verifies a capability given as the subject token, to
	// narrow (Narrow); nil narrows none. CapabilityAudience is the audience
	// such a capability must name: the exchange's own. NarrowPolicy decides
	// each narrowing. All three, or none. A bound capability's proof is
	// checked by Capabilities' own DPoP verifier: give it the nonces of DPoP,
	// so a nonce this endpoint hands out is one that verifier takes.
	Capabilities       *limes.Verifier
	CapabilityAudience string
	NarrowPolicy       NarrowPolicy
	// Exchanges, when set, makes the Exchanger issue one capability per
	// subject token, by its issuer and jti within the tenant the policy
	// names (docs/adr/0009-one-capability-per-subject-token.md): the first
	// exchange issues and records it; every later one, from any replica,
	// hands the same capability back, its token signed again from the
	// record (limes.Issuer.Reissue), so its ceilings are counted once. In
	// this mode a subject token without a jti is refused; an exchange of a
	// capability revoked or expired is refused, never issued anew; one with
	// a proof by another key than the capability is bound to, or none for a
	// bound one, or one for a bearer one, is refused; one asking for other
	// targets than the capability's audience is refused; and a policy that
	// names no TTL gets the subject token's remaining lifetime, not
	// DefaultTTL. The policy still runs on every exchange: it names the
	// tenant. Nil: every exchange issues a capability of its own. The
	// Issuer's store must read records back (limes.RecordReader), as every
	// limes.Store does.
	Exchanges limes.ExchangeStore
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
	// Logger logs every exchange and refusal. Nil: slog.Default().
	Logger *slog.Logger
}

// Exchanger exchanges subject tokens for capabilities.
type Exchanger struct {
	cfg Config
}

// NewExchanger builds an Exchanger, refusing a config without an issuer, a
// subject verifier or a policy.
func NewExchanger(cfg Config) (*Exchanger, error) {
	switch {
	case cfg.Issuer == nil:
		return nil, fmt.Errorf("%w: exchange: Config.Issuer required", limes.ErrInvalidConfig)
	case cfg.Subjects == nil:
		return nil, fmt.Errorf("%w: exchange: Config.Subjects required", limes.ErrInvalidConfig)
	case cfg.Policy == nil:
		return nil, fmt.Errorf("%w: exchange: Config.Policy required", limes.ErrInvalidConfig)
	case (cfg.Capabilities != nil) != (cfg.CapabilityAudience != "") || (cfg.Capabilities != nil) != (cfg.NarrowPolicy != nil):
		return nil, fmt.Errorf("%w: exchange: Config.Capabilities, CapabilityAudience and NarrowPolicy go together", limes.ErrInvalidConfig)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Exchanger{cfg: cfg}, nil
}

// Exchange exchanges req's subject token for a capability: it verifies the
// token and that the client may present it, checks the proof of possession,
// asks the policy, holds the policy's answer to the request, records the
// proof and issues. A refusal is one of the errors above, or a dpop error
// for the proof; every exchange and refusal is logged.
func (e *Exchanger) Exchange(ctx context.Context, req Request) (Result, error) {
	res, subject, err := e.exchange(ctx, req)
	log := e.cfg.Logger.With(slog.String("client", req.Client.ID))
	if subject.Subject != "" {
		log = log.With(slog.String("subject_issuer", subject.Issuer), slog.String("subject", subject.Subject))
	}
	if err != nil {
		level := slog.LevelWarn
		if !isRefusal(err) {
			level = slog.LevelError
		}
		log.Log(ctx, level, "exchange refused", slog.Any("error", err))
		return Result{}, err
	}
	log.InfoContext(ctx, "exchange granted",
		slog.String("capability", res.Capability.ID.String()),
		slog.Any("audience", res.Capability.Audience),
		slog.String("token_type", res.TokenType),
		slog.Bool("reissued", res.Reissued),
		slog.Time("expires_at", res.Capability.ExpiresAt))
	return res, nil
}

// exchange is Exchange, and the subject once verified, for the log.
func (e *Exchanger) exchange(ctx context.Context, req Request) (Result, Subject, error) {
	if err := checkExchangeRequest(req); err != nil {
		return Result{}, Subject{}, err
	}
	subject, remaining, err := e.verifySubject(ctx, req)
	if err != nil {
		return Result{}, subject, err
	}
	if e.cfg.Exchanges != nil && subject.ID == "" {
		return Result{}, subject, fmt.Errorf("%w: no jti, and each subject token is exchanged for one capability", ErrInvalidGrant)
	}
	checked, bound, err := e.checkProof(ctx, req, subject)
	if err != nil {
		return Result{}, subject, err
	}
	issue, err := e.grant(ctx, req, subject, remaining)
	if err != nil {
		return Result{}, subject, err
	}
	if bound {
		issue.ConfirmationJKT = checked.JKT()
	}
	key := limes.ExchangeKey{TenantID: issue.Subject.TenantID, Issuer: subject.Issuer, TokenID: subject.ID}
	if e.cfg.Exchanges != nil {
		res, found, err := e.exchangedBefore(ctx, key, issue)
		if err != nil || found {
			if err == nil && bound {
				err = e.cfg.DPoP.Record(ctx, checked)
			}
			return res, subject, err
		}
	}
	if bound {
		// Recorded only now, once everything else passed, and before the
		// capability exists: a replayed proof never mints one.
		if err := e.cfg.DPoP.Record(ctx, checked); err != nil {
			return Result{}, subject, err
		}
	}
	minted, err := e.cfg.Issuer.Issue(ctx, issue)
	if err != nil {
		return Result{}, subject, fmt.Errorf("exchange: issue: %w", err)
	}
	if e.cfg.Exchanges == nil {
		return resultOf(minted), subject, nil
	}
	res, err := e.claim(ctx, key, issue, minted)
	return res, subject, err
}

// exchangedBefore is the capability an earlier exchange of the subject
// token issued under key, reissued, with found true; found is false when
// there is none.
func (e *Exchanger) exchangedBefore(ctx context.Context, key limes.ExchangeKey, issue limes.IssueRequest) (Result, bool, error) {
	id, err := e.cfg.Exchanges.ExchangedFor(ctx, key)
	if errors.Is(err, limes.ErrNotFound) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("exchange: the earlier exchange of the subject token: %w", err)
	}
	res, err := e.reissue(ctx, id, issue)
	return res, true, err
}

// claim records minted as the capability of key. When another exchange of
// the same subject token claimed key first, its capability is reissued and
// minted is left to expire: its token never left this process, so nothing
// can present it.
func (e *Exchanger) claim(ctx context.Context, key limes.ExchangeKey, issue limes.IssueRequest, minted limes.IssueResult) (Result, error) {
	recorded, claimed, err := e.cfg.Exchanges.ClaimExchange(ctx, key, minted.Capability.ID)
	if err != nil {
		return Result{}, fmt.Errorf("exchange: record the exchange of the subject token: %w", err)
	}
	if claimed {
		return resultOf(minted), nil
	}
	e.cfg.Logger.InfoContext(ctx, "exchange lost a race for its subject token; the capability it issued is left to expire unused",
		slog.String("capability", minted.Capability.ID.String()), slog.String("winner", recorded.String()))
	return e.reissue(ctx, recorded, issue)
}

// reissue hands back the capability id an earlier exchange issued, held to
// this exchange: live, bound to the key this request proves, if any, and
// for no audience beyond this request's targets.
func (e *Exchanger) reissue(ctx context.Context, id uuid.UUID, issue limes.IssueRequest) (Result, error) {
	again, err := e.cfg.Issuer.Reissue(ctx, id)
	switch {
	case errors.Is(err, limes.ErrRevoked), errors.Is(err, limes.ErrExpired):
		return Result{}, errs.Errorf("%w: the subject token's capability is no longer live: %w", ErrInvalidGrant, err)
	case err != nil:
		return Result{}, fmt.Errorf("exchange: reissue: %w", err)
	}
	c := again.Capability
	if c.ConfirmationJKT != issue.ConfirmationJKT {
		return Result{}, fmt.Errorf("%w: the subject token's capability is bound to another key than this request proves, or to none", ErrInvalidGrant)
	}
	if !slices.Equal(sortedCopy(c.Audience), sortedCopy(issue.Audience)) {
		return Result{}, fmt.Errorf("%w: the subject token's capability is for %q, not %q", ErrInvalidTarget, c.Audience, issue.Audience)
	}
	res := resultOf(again)
	res.Reissued = true
	return res, nil
}

// sortedCopy is s sorted, s left as it is.
func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// checkExchangeRequest refuses an exchange without a client that may present
// subject tokens, without a subject token, or asking for no target.
func checkExchangeRequest(req Request) error {
	switch {
	case req.Client.ID == "" || len(req.Client.SubjectAudiences) == 0:
		return fmt.Errorf("%w: a client with an id and the audiences it may present", ErrInvalidClient)
	case req.SubjectToken == "":
		return fmt.Errorf("%w: subject_token required", ErrInvalidRequest)
	}
	return requireTarget(req.Resources, req.Audiences)
}

// requireTarget refuses a request asking for neither a resource nor an
// audience: a capability is for somewhere.
func requireTarget(resources, audiences []string) error {
	if len(resources) == 0 && len(audiences) == 0 {
		return fmt.Errorf("%w: a resource or an audience required", ErrInvalidTarget)
	}
	return nil
}

// verifySubject verifies req's subject token, and returns it with the time
// it has left, refusing one issued for another client or about to expire.
func (e *Exchanger) verifySubject(ctx context.Context, req Request) (Subject, time.Duration, error) {
	subject, err := e.cfg.Subjects.VerifySubject(ctx, req.SubjectToken)
	if err != nil {
		if !errors.Is(err, ErrInvalidGrant) {
			return Subject{}, 0, fmt.Errorf("exchange: verify subject token: %w", err)
		}
		return Subject{}, 0, err
	}
	if !slices.ContainsFunc(subject.Audience, func(aud string) bool { return slices.Contains(req.Client.SubjectAudiences, aud) }) {
		return subject, 0, fmt.Errorf("%w: issued for %q, not for the client", ErrInvalidGrant, subject.Audience)
	}
	remaining := subject.ExpiresAt.Sub(e.cfg.Now()).Truncate(time.Second)
	if remaining < minTTL {
		return subject, 0, fmt.Errorf("%w: expires within %s", ErrInvalidGrant, minTTL)
	}
	return subject, remaining, nil
}

// grant is the issue request the policy makes of req, held to the
// exchange: its targets, the subject token's client and subject, and a
// lifetime within the subject token's remaining.
func (e *Exchanger) grant(ctx context.Context, req Request, subject Subject, remaining time.Duration) (limes.IssueRequest, error) {
	issue, err := e.cfg.Policy(ctx, Grant{
		Subject: subject, Client: req.Client, Resources: slices.Clone(req.Resources), Audiences: slices.Clone(req.Audiences),
		Scopes: slices.Clone(req.Scopes), AuthorizationDetails: slices.Clone(req.AuthorizationDetails),
	})
	if err != nil {
		return limes.IssueRequest{}, err
	}
	if issue.Audience, err = holdPolicy(issue.Audience, issue.ConfirmationJKT, targetsOf(req.Resources, req.Audiences)); err != nil {
		return limes.IssueRequest{}, err
	}
	// The identity provider's session is the capability's, unless the
	// policy names another, so ending the session revokes it.
	if issue.Session == "" {
		issue.Session = subject.SessionID
	}
	// The client the identity provider issued the subject token to is the
	// capability's, unless the policy names another.
	if issue.ClientID == "" {
		issue.ClientID = subject.ClientID
	}
	// A capability acting on behalf of another acts for the subject token's
	// subject alone: naming anyone else would have the exchange speak for
	// someone who presented nothing.
	if issue.OnBehalfOf != nil && issue.OnBehalfOf.Subject != subject.Subject {
		return limes.IssueRequest{}, fmt.Errorf("%w: it acts on behalf of %q, not the subject token's %q", errPolicy, issue.OnBehalfOf.Subject, subject.Subject)
	}
	if issue.TTL == 0 {
		issue.TTL = DefaultTTL
		if e.cfg.Exchanges != nil {
			// One capability per subject token: one shorter than the token
			// would leave the rest of its life refused.
			issue.TTL = remaining
		}
	}
	issue.TTL = min(issue.TTL, remaining)
	return issue, nil
}

// targetsOf is what a request asks for: its resources, then its audiences.
func targetsOf(resources, audiences []string) []string {
	return append(slices.Clone(resources), audiences...)
}

// holdPolicy is the audience a policy asked for, targets when it asked for
// none, refused when it reaches past targets or binds the capability itself,
// which only the request's proof may.
func holdPolicy(audience []string, jkt string, targets []string) ([]string, error) {
	if len(audience) == 0 {
		audience = targets
	}
	for _, aud := range audience {
		if !slices.Contains(targets, aud) {
			return nil, fmt.Errorf("%w: audience %q was not asked for", errPolicy, aud)
		}
	}
	if jkt != "" {
		return nil, fmt.Errorf("%w: it binds the capability itself", errPolicy)
	}
	return audience, nil
}

// resultOf is the answer for a minted capability: a DPoP token when it is
// bound to a key, a bearer token otherwise.
func resultOf(minted limes.IssueResult) Result {
	tokenType := TokenTypeBearer
	if minted.Capability.ConfirmationJKT != "" {
		tokenType = TokenTypeDPoP
	}
	return Result{Capability: minted.Capability, Token: minted.Token, TokenType: tokenType}
}

// checkProof checks req's proof of possession, if it has one, as a token
// request's; a subject token bound to a key needs a proof by that key.
// bound reports whether the capability is to be bound to checked's key.
func (e *Exchanger) checkProof(ctx context.Context, req Request, subject Subject) (checked dpop.CheckedProof, bound bool, err error) {
	if req.DPoP.JWT == "" {
		if subject.JKT != "" {
			return dpop.CheckedProof{}, false, fmt.Errorf("%w: the subject token is bound to a key", dpop.ErrRequired)
		}
		return dpop.CheckedProof{}, false, nil
	}
	if e.cfg.DPoP == nil {
		return dpop.CheckedProof{}, false, fmt.Errorf("%w: this endpoint binds no capability to a key", dpop.ErrInvalid)
	}
	checked, err = e.cfg.DPoP.CheckTokenRequest(ctx, dpop.TokenRequest{Proof: req.DPoP, Holder: req.Client.ID})
	if err != nil {
		return dpop.CheckedProof{}, false, err
	}
	if subject.JKT != "" && checked.JKT() != subject.JKT {
		return dpop.CheckedProof{}, false, fmt.Errorf("%w: the proof is not by the key the subject token is bound to", dpop.ErrInvalid)
	}
	return checked, true, nil
}

// isRefusal reports whether err is the client's: a refusal rather than the
// server's failure.
func isRefusal(err error) bool {
	for _, refusal := range []error{
		ErrInvalidRequest, ErrInvalidClient, ErrInvalidGrant, ErrInvalidTarget, ErrInvalidScope, ErrInvalidAuthorizationDetails,
		ErrUnsupportedGrantType, ErrUnauthorizedClient,
		dpop.ErrRequired, dpop.ErrInvalid, dpop.ErrReplayed, dpop.ErrNonceRequired,
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}
