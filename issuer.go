package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
)

// Issuer mints capabilities, in this order:
//
//  1. Validate the request (caveats sane, parent narrowing if delegated).
//  2. Sign the token, then persist the record, and only then return the
//     token.
//
// Signing first means a capability whose token cannot be minted — a signer
// that fails, or a token past MaxTokenBytes — is never recorded. The token
// is returned only once its record is written: if the store write fails, the
// signed token is dropped, so no token leaves the issuer that audit could
// not find or Revoke, which refuses an id not on record, could not stop.
type Issuer struct {
	signer *Signer
	store  IssuanceStore
	clock  func() time.Time

	issuerName    string
	defaultTTL    time.Duration
	maxTokenBytes int
	// form seals a capability in the Biscuit form (Biscuit).
	form TokenForm
}

// DefaultIssueTTL is IssuerConfig.DefaultTTL when it is zero.
const DefaultIssueTTL = 15 * time.Minute

// IssuerConfig is the wiring for NewIssuer.
type IssuerConfig struct {
	// Signer mints tokens (NewSigner), over a key in memory or one held in
	// a KMS or an HSM.
	Signer *Signer

	// Store records what the issuer mints and answers whether a parent is
	// revoked. Any Store is one.
	Store IssuanceStore

	// IssuerName is the value placed in the `iss` claim. Verifiers
	// require this to be in their TrustedIssuers list.
	IssuerName string

	// DefaultTTL caps the lifetime when IssueRequest.TTL is zero.
	// Zero: DefaultIssueTTL, short enough that revocation propagation
	// latency rarely matters. Negative is refused.
	DefaultTTL time.Duration

	// MaxTokenBytes refuses to mint a token longer than this, which a
	// verifier with the same limit (VerifierConfig.MaxTokenBytes) would
	// refuse unread. Zero: DefaultMaxTokenBytes; negative is refused.
	MaxTokenBytes int

	// Now is the clock; tests override.
	Now func() time.Time

	// Biscuit is the form Issuer.Biscuit seals a capability in. Nil: the
	// Biscuit form this module carries.
	Biscuit TokenForm
}

// NewIssuer validates wiring and returns a ready Issuer.
func NewIssuer(cfg IssuerConfig) (*Issuer, error) {
	if cfg.Signer == nil {
		return nil, errors.New("limes: IssuerConfig.Signer required")
	}
	if cfg.Store == nil {
		return nil, errors.New("limes: IssuerConfig.Store required")
	}
	if cfg.IssuerName == "" {
		return nil, errors.New("limes: IssuerConfig.IssuerName required")
	}
	if cfg.DefaultTTL < 0 {
		return nil, fmt.Errorf("limes: IssuerConfig.DefaultTTL %s is negative", cfg.DefaultTTL)
	}
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = DefaultIssueTTL
	}
	if cfg.MaxTokenBytes < 0 {
		return nil, fmt.Errorf("limes: IssuerConfig.MaxTokenBytes %d is negative", cfg.MaxTokenBytes)
	}
	if cfg.MaxTokenBytes == 0 {
		cfg.MaxTokenBytes = DefaultMaxTokenBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Biscuit == nil {
		cfg.Biscuit = biscuitForm{}
	}
	return &Issuer{
		signer:        cfg.Signer,
		store:         cfg.Store,
		clock:         cfg.Now,
		issuerName:    cfg.IssuerName,
		defaultTTL:    cfg.DefaultTTL,
		maxTokenBytes: cfg.MaxTokenBytes,
		form:          cfg.Biscuit,
	}, nil
}

// fits refuses a token longer than MaxTokenBytes: one a verifier refuses
// unread is a capability nobody can use.
func (i *Issuer) fits(token string) error {
	if len(token) > i.maxTokenBytes {
		return invalidRequest("the token would be %d bytes, past MaxTokenBytes %d; narrow its caveats", len(token), i.maxTokenBytes)
	}
	return nil
}

// sign signs c and holds the token to MaxTokenBytes, before anything is
// recorded.
func (i *Issuer) sign(ctx context.Context, c Capability) (string, error) {
	token, err := i.signer.sign(ctx, c)
	if err != nil {
		return "", fmt.Errorf("limes: sign: %w", err)
	}
	if err := i.fits(token); err != nil {
		return "", err
	}
	return token, nil
}

// IssueRequest is the input shape for Issuer.Issue. Subject (with its
// TenantID), IssuedBy, Audience and Caveats are required; TTL, NotBefore
// and ConfirmationJKT are optional, and the issuer fills in ID, IssuedAt,
// ExpiresAt and Issuer. Caveats are
// validated (Caveats.Validate) before anything is persisted.
type IssueRequest struct {
	Subject Principal
	// IssuedBy is the principal REQUESTING the capability — an operator for a
	// root issuance, the parent's holder for a delegation. Distinct from
	// Subject, which is who the capability authorises. Its Subject is
	// required — a capability nobody requested cannot be audited — and the
	// rest is recorded as given: an operator need not be a tenant's
	// principal.
	IssuedBy  Principal
	Audience  []string
	Caveats   Caveats
	TTL       time.Duration // 0 → DefaultTTL; must reach the next whole second
	NotBefore time.Time     // zero → valid from issuance, no nbf claim
	// ConfirmationJKT binds the capability to the key with this RFC 7638
	// thumbprint (dpop.Thumbprint); empty issues a bearer capability.
	ConfirmationJKT string
	// ClientID is the OAuth client the capability is granted to
	// (Capability.ClientID): the client of the access token it was exchanged
	// for, or the software the subject runs as. Required.
	ClientID string
	// OnBehalfOf is the principal Subject acts for (Capability.OnBehalfOf),
	// of Subject's tenant; nil when Subject acts for itself.
	OnBehalfOf *Principal
}

// parentRunOf is the run a capability of p delegates from: an agent's RunID,
// zero for any other principal.
func parentRunOf(p Principal) uuid.UUID {
	if p.Agent == nil {
		return uuid.Nil()
	}
	return p.Agent.RunID
}

// IssueResult is what Issue and Delegate mint: the capability as recorded,
// to log or display, and its compact-form token, to ship to the agent.
type IssueResult struct {
	Capability Capability
	Token      string
	_          noCompare
}

// Issue mints a top-level capability.
func (i *Issuer) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	return result(i.issue(ctx, req))
}

// result is a minted capability and its token as an IssueResult.
func result(c Capability, token string, err error) (IssueResult, error) {
	if err != nil {
		return IssueResult{}, err
	}
	return IssueResult{Capability: c, Token: token}, nil
}

// issue is Issue, returning the capability and its token apart.
func (i *Issuer) issue(ctx context.Context, req IssueRequest) (Capability, string, error) {
	if err := checkIssueRequest(req); err != nil {
		return Capability{}, "", err
	}
	now := i.clock().UTC()
	expires, err := i.expiry(now, req.TTL)
	if err != nil {
		return Capability{}, "", err
	}
	cap := i.mint(grantTerms{
		Subject: req.Subject, Audience: req.Audience, Caveats: req.Caveats,
		ConfirmationJKT: req.ConfirmationJKT, IssuedAt: now, ExpiresAt: expires, ClientID: req.ClientID,
		OnBehalfOf: req.OnBehalfOf,
	})
	if err := checkLifetime(cap); err != nil {
		return Capability{}, "", err
	}
	if err := setNotBefore(&cap, req.NotBefore); err != nil {
		return Capability{}, "", err
	}

	token, err := i.signAndRecord(ctx, cap, req.IssuedBy, "issuance")
	if err != nil {
		return Capability{}, "", err
	}
	return cap, token, nil
}

// checkIssueRequest refuses a request missing whom the capability is for,
// who asks, or its client, or one whose terms are malformed.
func checkIssueRequest(req IssueRequest) error {
	if err := checkIssueSubject(req); err != nil {
		return err
	}
	if req.IssuedBy.Subject == "" {
		return invalidRequest("IssuedBy is required")
	}
	if req.ClientID == "" {
		return invalidRequest("ClientID is required (RFC 9068 client_id)")
	}
	return validateTerms(req.Audience, req.Caveats, req.ConfirmationJKT)
}

// checkIssueSubject refuses a subject without a tenant or a sub, malformed,
// or acting for a principal it may not act for.
func checkIssueSubject(req IssueRequest) error {
	if req.Subject.TenantID == uuid.Nil() {
		return invalidRequest("Issue requires Subject.TenantID")
	}
	if req.Subject.Subject == "" {
		return invalidRequest("Issue requires Subject.Subject (RFC 9068 sub)")
	}
	if err := req.Subject.Validate(); err != nil {
		return err
	}
	if req.OnBehalfOf == nil {
		return nil
	}
	return validateOnBehalfOf(*req.OnBehalfOf, req.Subject)
}

// validateTerms checks what an issued and a delegated capability both
// carry, in this order: its audience, its caveats and its key binding.
func validateTerms(audience []string, caveats Caveats, jkt string) error {
	if err := validateAudience(audience); err != nil {
		return err
	}
	if err := caveats.Validate(); err != nil {
		return err
	}
	if err := checkUsable(caveats.Requests); err != nil {
		return err
	}
	return validateThumbprint(jkt)
}

// checkUsable refuses, as ErrInvalidCaveats, a capability whose request
// ceiling allows no request: every meter refuses its first use, so nothing
// can be done with it. Set Requests explicitly, Unlimited or a Max.
func checkUsable(requests RequestCeiling) error {
	if !requests.Unlimited && requests.Max == 0 {
		return fmt.Errorf("%w: requests: a ceiling of no request leaves the capability unusable; set Max or Unlimited", ErrInvalidCaveats)
	}
	return nil
}

// grantTerms are the fields of a capability its request decides.
type grantTerms struct {
	Subject         Principal
	Audience        []string
	Caveats         Caveats
	ConfirmationJKT string
	IssuedAt        time.Time
	ExpiresAt       time.Time
	ParentID        uuid.UUID
	ClientID        string
	OnBehalfOf      *Principal
	PriorActors     []string
}

// mint is a capability of t under a fresh id, issued by i. It holds copies
// of t's slices and pointers, so the caller's request cannot alter it.
func (i *Issuer) mint(t grantTerms) Capability {
	var onBehalfOf *Principal
	if t.OnBehalfOf != nil {
		obo := t.OnBehalfOf.clone()
		onBehalfOf = &obo
	}
	return Capability{
		ID:              uuid.NewV7(),
		Issuer:          i.issuerName,
		Subject:         t.Subject.clone(),
		Audience:        slices.Clone(t.Audience),
		Caveats:         t.Caveats.clone(),
		IssuedAt:        t.IssuedAt.Truncate(tokenTimeResolution),
		ExpiresAt:       t.ExpiresAt,
		ParentID:        t.ParentID,
		ConfirmationJKT: t.ConfirmationJKT,
		ClientID:        t.ClientID,
		OnBehalfOf:      onBehalfOf,
		PriorActors:     slices.Clone(t.PriorActors),
	}
}

// signAndRecord signs c and records it as issued by issuedBy, and returns
// its token. Signed first: a capability whose token cannot be minted, or
// would be too long to use, is never recorded. what names the record in an
// error from the store.
func (i *Issuer) signAndRecord(ctx context.Context, c Capability, issuedBy Principal, what string) (string, error) {
	token, err := i.sign(ctx, c)
	if err != nil {
		return "", err
	}
	if err := i.store.Insert(ctx, InsertRequest{Capability: c, IssuedBy: issuedBy}); err != nil {
		return "", fmt.Errorf("limes: persist %s: %w", what, err)
	}
	return token, nil
}

// expiry resolves a requested TTL against the default. A negative TTL is a
// request error rather than an already-expired capability.
func (i *Issuer) expiry(now time.Time, ttl time.Duration) (time.Time, error) {
	if ttl < 0 {
		return time.Time{}, invalidRequest("TTL %s is negative", ttl)
	}
	if ttl == 0 {
		ttl = i.defaultTTL
	}
	return now.Add(ttl).Truncate(tokenTimeResolution), nil
}

// checkLifetime refuses a capability that would expire no later than it is
// issued: a TTL ending within the second leaves a token whose whole second
// has already ended, valid only by leeway. Delegate needs no such check: a
// parent's expiry is a whole second, and one already reached is refused.
func checkLifetime(c Capability) error {
	if !c.ExpiresAt.After(c.IssuedAt) {
		return invalidRequest("the capability would expire at %s, no later than it is issued: a token's times are whole seconds", c.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

// tokenTimeResolution is what a token's times carry: JWT NumericDates are
// whole seconds. The issuer records each time as the token carries it —
// issued and expiring rounded down, valid from rounded up — so the record
// and the token never disagree, and no time leaves the issuer earlier than
// the token allows or later than it ends.
const tokenTimeResolution = time.Second

// setNotBefore applies a requested not-before time, refusing one at or after
// the expiry: such a capability could never be used.
func setNotBefore(c *Capability, nbf time.Time) error {
	if nbf.IsZero() {
		return nil
	}
	nbf = nbf.UTC()
	if t := nbf.Truncate(tokenTimeResolution); !t.Equal(nbf) {
		nbf = t.Add(tokenTimeResolution) // up: never valid before it was asked to be
	}
	if !nbf.Before(c.ExpiresAt) {
		return invalidRequest("NotBefore %s is not before ExpiresAt %s", nbf, c.ExpiresAt)
	}
	c.NotBefore = nbf
	return nil
}

// validateAudience requires a non-empty audience of non-empty entries.
func validateAudience(aud []string) error {
	if len(aud) == 0 {
		return invalidRequest("non-empty Audience required")
	}
	if slices.Contains(aud, "") {
		return invalidRequest("Audience contains an empty entry")
	}
	return nil
}

// DelegateRequest narrows from a parent capability the caller already
// holds. The issuer enforces narrowing (Narrows) before persisting so a
// runtime widening attempt fails before any token is emitted.
type DelegateRequest struct {
	// Parent is the capability the child narrows, as the verifier accepted
	// it: only Verifier.Verify and Authorize make one.
	Parent VerifiedCapability
	// Subject is who the child authorises; its TenantID defaults to the
	// parent's.
	Subject Principal
	// Audience defaults to the parent's, and must be a subset of it.
	Audience []string
	// Caveats are the child's; see InheritCaveats.
	Caveats Caveats
	// InheritCaveats copies the parent's caveats verbatim and ignores
	// Caveats. It must be asked for: Caveats with no Ops is refused, never
	// read as "inherit", which would replace every other field the caller
	// set — a budget of 2 would become the parent's 25 without a word.
	InheritCaveats bool
	// TTL defaults to IssuerConfig.DefaultTTL, and is cut so the child
	// expires no later than the parent.
	TTL time.Duration
	// NotBefore is raised, when earlier, to the parent's start: its
	// NotBefore, else its IssuedAt.
	NotBefore time.Time
	// ConfirmationJKT binds the child to a key — typically the sub-agent's.
	// Empty keeps the parent's binding, if it has one: a bound parent's
	// child is always bound.
	ConfirmationJKT string
	// ClientID is the OAuth client the child is granted to. Empty: the
	// parent's.
	ClientID string
}

// Delegate mints a child capability narrower than the supplied parent, which
// the verifier accepted (VerifiedCapability), so its caveats are the ones its
// token carries. It does not trust it to be current, though: a parent that
// has expired,
// or that the store reports revoked (itself or any ancestor), delegates
// nothing; nor does a Biscuit copy (VerifiedCapability.FromBiscuit), which
// is ErrInvalidRequest, as Issuer.Biscuit answers it — its holder
// attenuates it instead. A parent
// another issuer named is ErrInvalidRequest: this issuer's store holds no
// record of it, so revoking it there would never reach the child. A child
// that would have more than MaxDelegationDepth ancestors is
// ErrDelegationTooDeep, refused by Store.Insert before it is on record. A parent whose caveats do not allow
// OpShare — the right to delegate — is ErrOpNotAllowed: delegating is the
// share operation, and a capability granted without it may not have the
// issuer mint a new principal any part of it.
func (i *Issuer) Delegate(ctx context.Context, req DelegateRequest) (IssueResult, error) {
	return result(i.delegate(ctx, req))
}

// delegate is Delegate, returning the capability and its token apart.
func (i *Issuer) delegate(ctx context.Context, req DelegateRequest) (Capability, string, error) {
	parent := req.Parent.c
	if err := i.checkDelegable(req.Parent); err != nil {
		return Capability{}, "", err
	}
	req, err := childRequest(req, parent)
	if err != nil {
		return Capability{}, "", err
	}
	now := i.clock().UTC()
	if err := i.checkLive(ctx, parent, now, "delegate from parent"); err != nil {
		return Capability{}, "", err
	}
	child, err := i.child(req, parent, now)
	if err != nil {
		return Capability{}, "", err
	}
	if err := Narrows(parent, child); err != nil {
		return Capability{}, "", fmt.Errorf("limes: delegate %w", err)
	}
	// A delegation is requested by whoever holds the parent — that is not a
	// caller-supplied fact, it is what delegation means, so it is derived
	// rather than accepted as an argument.
	token, err := i.signAndRecord(ctx, child, parent.Subject, "delegation")
	if err != nil {
		return Capability{}, "", err
	}
	// Insert refuses a child past MaxDelegationDepth. A store that does not
	// is still caught here, before a token no verifier would accept leaves:
	// the walk reaches past the bound only from a chain too deep.
	if _, err := i.store.IsRevoked(ctx, child.ID); err != nil {
		return Capability{}, "", fmt.Errorf("limes: delegate from parent %s: %w", parent.ID, err)
	}
	return child, token, nil
}

// checkDelegable refuses a parent this issuer delegates nothing from: one
// the verifier did not accept, a Biscuit copy, another issuer's, or one
// whose caveats do not allow OpShare.
func (i *Issuer) checkDelegable(p VerifiedCapability) error {
	parent := p.c
	switch {
	case parent.ID == uuid.Nil():
		return invalidRequest("Delegate requires a Parent the verifier accepted")
	case p.fromBiscuit || len(p.copies) > 0:
		// A Biscuit copy delegates nothing: the child would be an ordinary
		// token of the capability, which neither RevokeBiscuit of the copy
		// reaches nor the copy's own counters bound. Its holder attenuates
		// it instead.
		return invalidRequest("parent is a Biscuit copy; attenuate it instead")
	case parent.Issuer != i.issuerName:
		return invalidRequest("parent %s was issued by %q, not %q", parent.ID, parent.Issuer, i.issuerName)
	case !slices.Contains(parent.Caveats.Ops, OpShare):
		return fmt.Errorf("%w: parent %s does not allow %q", ErrOpNotAllowed, parent.ID, OpShare)
	}
	return nil
}

// childRequest is req with what it leaves out taken from parent — the
// tenant, the audience, the caveats when inherited, the budget's unit —
// refused when the child it asks for is malformed.
func childRequest(req DelegateRequest, parent Capability) (DelegateRequest, error) {
	if req.Subject.TenantID == uuid.Nil() {
		req.Subject.TenantID = parent.Subject.TenantID
	}
	if req.Subject.Subject == "" {
		return req, invalidRequest("Delegate requires Subject.Subject (RFC 9068 sub)")
	}
	if err := req.Subject.Validate(); err != nil {
		return req, err
	}
	if err := checkChildRun(req.Subject.Agent, parentRunOf(parent.Subject)); err != nil {
		return req, err
	}
	if len(req.Audience) == 0 {
		req.Audience = append([]string(nil), parent.Audience...)
	}
	if req.InheritCaveats {
		req.Caveats = parent.Caveats
	}
	// A child counts in its parent's unit; one that names none takes it,
	// before its caveats are validated against it.
	if req.Caveats.Budget.UnitCode == "" {
		req.Caveats.Budget.UnitCode = parent.Caveats.Budget.UnitCode
	}
	return req, validateTerms(req.Audience, req.Caveats, req.ConfirmationJKT)
}

// checkChildRun refuses a child agent whose runs contradict its parent's
// run: a parent run other than it, or the parent's own run as its run.
func checkChildRun(a *AgentPrincipal, parentRun uuid.UUID) error {
	if a == nil {
		return nil
	}
	if a.ParentRunID != uuid.Nil() && a.ParentRunID != parentRun {
		return invalidRequest("Subject.Agent.ParentRunID %s is not the parent's RunID %s", a.ParentRunID, parentRun)
	}
	// A child run is another run than the one it was delegated from: the
	// same id would make the run its own parent in an audit's run graph.
	if parentRun != uuid.Nil() && a.RunID == parentRun {
		return invalidRequest("Subject.Agent.RunID %s is the parent's own run", a.RunID)
	}
	return nil
}

// checkLive refuses deriving anything from c, as what names the derivation
// ("delegate from parent", "biscuit of"), once c has expired at now or is
// revoked.
func (i *Issuer) checkLive(ctx context.Context, c Capability, now time.Time, what string) error {
	if !now.Before(c.ExpiresAt) {
		return fmt.Errorf("limes: %s %s: %w", what, c.ID, ErrExpired)
	}
	revoked, err := i.store.IsRevoked(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("limes: %s %s: revocation lookup: %w", what, c.ID, err)
	}
	if revoked {
		return fmt.Errorf("limes: %s %s: %w", what, c.ID, ErrRevoked)
	}
	return nil
}

// child mints the capability req asks for under parent: ending no later
// than parent, taking what it leaves out from parent, starting no earlier.
func (i *Issuer) child(req DelegateRequest, parent Capability, now time.Time) (Capability, error) {
	expires, err := i.expiry(now, req.TTL)
	if err != nil {
		return Capability{}, err
	}
	if expires.After(parent.ExpiresAt) {
		expires = parent.ExpiresAt
	}
	onBehalfOf, prior := actingOf(parent)
	child := i.mint(grantTerms{
		Subject: req.Subject, Audience: req.Audience, Caveats: req.Caveats,
		ConfirmationJKT: req.ConfirmationJKT, IssuedAt: now, ExpiresAt: expires, ParentID: parent.ID,
		ClientID: req.ClientID, OnBehalfOf: &onBehalfOf, PriorActors: prior,
	})
	inheritFrom(&child, parent)
	if err := setNotBefore(&child, childNotBefore(req.NotBefore, now, parent)); err != nil {
		return Capability{}, err
	}
	return child, nil
}

// inheritFrom gives child what it does not name from parent: its key
// binding, its client, and, for an agent, the run it was delegated from.
func inheritFrom(child *Capability, parent Capability) {
	if child.ConfirmationJKT == "" {
		child.ConfirmationJKT = parent.ConfirmationJKT
	}
	if child.ClientID == "" {
		child.ClientID = parent.ClientID
	}
	// The run a child agent was delegated from is the parent's, taken from
	// the token the verifier accepted rather than on the caller's word.
	parentRun := parentRunOf(parent.Subject)
	if child.Subject.Type == PrincipalTypeAgent && parentRun != uuid.Nil() {
		if child.Subject.Agent == nil {
			child.Subject.Agent = &AgentPrincipal{}
		}
		child.Subject.Agent.ParentRunID = parentRun
	}
}

// childNotBefore is the not-before of a child asked to start at requested:
// a child starts no earlier than its parent, as it ends no later. The parent
// may start after now — a not-before ahead, or an issuer replica whose clock
// runs ahead of this one — and the child then waits for it.
func childNotBefore(requested, now time.Time, parent Capability) time.Time {
	start := requested
	if start.IsZero() {
		start = now.Truncate(tokenTimeResolution) // as IssuedAt records it
	}
	if ps := validFrom(parent); start.Before(ps) {
		return ps
	}
	return requested
}

// actingOf is who a child of parent acts for, and through: the principal its
// parent acts for, with the parent's holder the nearest prior actor, or, when
// the parent acts for itself, its holder, with none prior. Derived, never
// asked for: a child acts for whoever its parent's authority came from.
func actingOf(parent Capability) (Principal, []string) {
	if parent.OnBehalfOf == nil {
		return parent.Subject.clone(), nil
	}
	return parent.OnBehalfOf.clone(), append([]string{parent.Subject.Subject}, parent.PriorActors...)
}

// GenerateKey makes a fresh Ed25519 signing key under the kid jwks.KeyID
// derives for it. It lives only as long as the process unless saved.
func GenerateKey() (SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return SigningKey{}, fmt.Errorf("limes: generate key: %w", err)
	}
	key := SigningKey{KeyID: jwks.KeyID(pub), Private: SealKey(priv)}
	// SealKey keeps a copy; the key is held there alone.
	clear(priv)
	return key, nil
}

// validateThumbprint accepts "" (unbound) or a base64url SHA-256 thumbprint.
// A malformed binding could match no key, minting a capability nobody can use.
func validateThumbprint(jkt string) error {
	if jkt != "" && !dpop.IsThumbprint(jkt) {
		return invalidRequest("ConfirmationJKT %q is not a base64url SHA-256 thumbprint", jkt)
	}
	return nil
}
