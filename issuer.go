package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

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
	return &Issuer{
		signer:        cfg.Signer,
		store:         cfg.Store,
		clock:         cfg.Now,
		issuerName:    cfg.IssuerName,
		defaultTTL:    cfg.DefaultTTL,
		maxTokenBytes: cfg.MaxTokenBytes,
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
// TenantID), IssuedBy, Audience and Caveats are required; TTL, NotBefore,
// Generation and ConfirmationJKT are optional, and the issuer fills in ID,
// IssuedAt, ExpiresAt and Issuer, and Generation when it is zero. Caveats are
// validated (Caveats.Validate) before anything is persisted.
type IssueRequest struct {
	Subject Principal
	// IssuedBy is the principal REQUESTING the capability — an operator for a
	// root issuance, the parent's holder for a delegation. Distinct from
	// Subject, which is who the capability authorises. Its Subject is
	// required — a capability nobody requested cannot be audited — and the
	// rest is recorded as given: an operator need not be a tenant's
	// principal.
	IssuedBy   Principal
	Audience   []string
	Caveats    Caveats
	TTL        time.Duration // 0 → DefaultTTL; must reach the next whole second
	NotBefore  time.Time     // zero → valid from issuance, no nbf claim
	Generation int64         // 0 → DefaultGeneration
	// ConfirmationJKT binds the capability to the key with this RFC 7638
	// thumbprint (dpop.Thumbprint); empty issues a bearer capability.
	ConfirmationJKT string
}

// DefaultGeneration is the Generation of a capability issued without one,
// and of every delegated capability.
const DefaultGeneration = 1

// IssueResult is what Issue and Delegate mint: the capability as recorded,
// to log or display, and its compact-form token, to ship to the agent.
type IssueResult struct {
	Capability Capability
	Token      string
	_          noCompare
}

// Issue mints a top-level capability. Generation defaults to
// DefaultGeneration; see Capability.Generation for what it does and does not
// promise.
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
	if req.Subject.TenantID == uuid.Nil {
		return Capability{}, "", invalidRequest("Issue requires Subject.TenantID")
	}
	if err := req.Subject.Validate(); err != nil {
		return Capability{}, "", err
	}
	if req.IssuedBy.Subject == "" {
		return Capability{}, "", invalidRequest("IssuedBy is required")
	}
	if req.Generation < 0 || req.Generation > MaxIJSONInteger {
		return Capability{}, "", invalidRequest("Generation %d is outside 0..%d", req.Generation, int64(MaxIJSONInteger))
	}
	if err := validateAudience(req.Audience); err != nil {
		return Capability{}, "", err
	}
	if err := req.Caveats.Validate(); err != nil {
		return Capability{}, "", err
	}
	if err := validateThumbprint(req.ConfirmationJKT); err != nil {
		return Capability{}, "", err
	}

	now := i.clock().UTC()
	expires, err := i.expiry(now, req.TTL)
	if err != nil {
		return Capability{}, "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Capability{}, "", fmt.Errorf("limes: capability id: %w", err)
	}
	cap := Capability{
		ID:              id,
		Issuer:          i.issuerName,
		Subject:         req.Subject.clone(),
		Audience:        slices.Clone(req.Audience),
		Caveats:         req.Caveats.clone(),
		IssuedAt:        now.Truncate(tokenTimeResolution),
		ExpiresAt:       expires,
		Generation:      req.Generation,
		ConfirmationJKT: req.ConfirmationJKT,
	}
	if cap.Generation == 0 {
		cap.Generation = DefaultGeneration
	}
	if err := checkLifetime(cap); err != nil {
		return Capability{}, "", err
	}
	if err := setNotBefore(&cap, req.NotBefore); err != nil {
		return Capability{}, "", err
	}

	// Signed first: a capability whose token cannot be minted, or would be
	// too long to use, is never recorded.
	token, err := i.sign(ctx, cap)
	if err != nil {
		return Capability{}, "", err
	}
	if err := i.store.Insert(ctx, InsertRequest{Capability: cap, IssuedBy: req.IssuedBy}); err != nil {
		return Capability{}, "", fmt.Errorf("limes: persist issuance: %w", err)
	}
	return cap, token, nil
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
// ErrDelegationTooDeep: its record is written before the store can count
// them, and its token is not returned. A parent whose caveats do not allow
// OpShare — the right to delegate — is ErrOpNotAllowed: delegating is the
// share operation, and a capability granted without it may not hand on any
// part of itself.
func (i *Issuer) Delegate(ctx context.Context, req DelegateRequest) (IssueResult, error) {
	return result(i.delegate(ctx, req))
}

// delegate is Delegate, returning the capability and its token apart.
func (i *Issuer) delegate(ctx context.Context, req DelegateRequest) (Capability, string, error) {
	parent := req.Parent.c
	if parent.ID == uuid.Nil {
		return Capability{}, "", invalidRequest("Delegate requires a Parent the verifier accepted")
	}
	// A Biscuit copy delegates nothing: the child would be an ordinary
	// token of the capability, which neither RevokeBiscuit of the copy
	// reaches nor the copy's own counters bound. Its holder attenuates it
	// instead.
	if req.Parent.fromBiscuit || len(parent.Copies) > 0 {
		return Capability{}, "", invalidRequest("parent is a Biscuit copy; attenuate it instead")
	}
	if parent.Issuer != i.issuerName {
		return Capability{}, "", invalidRequest("parent %s was issued by %q, not %q", parent.ID, parent.Issuer, i.issuerName)
	}
	if !slices.Contains(parent.Caveats.Ops, OpShare) {
		return Capability{}, "", fmt.Errorf("%w: parent %s does not allow %q", ErrOpNotAllowed, parent.ID, OpShare)
	}
	if req.Subject.TenantID == uuid.Nil {
		req.Subject.TenantID = parent.Subject.TenantID
	}
	if err := req.Subject.Validate(); err != nil {
		return Capability{}, "", err
	}
	if len(req.Audience) == 0 {
		req.Audience = append([]string(nil), parent.Audience...)
	}
	if req.InheritCaveats {
		req.Caveats = parent.Caveats
	}
	if err := validateAudience(req.Audience); err != nil {
		return Capability{}, "", err
	}
	if err := req.Caveats.Validate(); err != nil {
		return Capability{}, "", err
	}
	if err := validateThumbprint(req.ConfirmationJKT); err != nil {
		return Capability{}, "", err
	}

	now := i.clock().UTC()
	if !now.Before(parent.ExpiresAt) {
		return Capability{}, "", fmt.Errorf("limes: delegate from parent %s: %w", parent.ID, ErrExpired)
	}
	revoked, err := i.store.IsRevoked(ctx, parent.ID)
	if err != nil {
		return Capability{}, "", fmt.Errorf("limes: delegate revocation lookup: %w", err)
	}
	if revoked {
		return Capability{}, "", fmt.Errorf("limes: delegate from parent %s: %w", parent.ID, ErrRevoked)
	}

	expires, err := i.expiry(now, req.TTL)
	if err != nil {
		return Capability{}, "", err
	}
	if expires.After(parent.ExpiresAt) {
		expires = parent.ExpiresAt
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Capability{}, "", fmt.Errorf("limes: capability id: %w", err)
	}

	child := Capability{
		ID:              id,
		Issuer:          i.issuerName,
		Subject:         req.Subject.clone(),
		Audience:        slices.Clone(req.Audience),
		Caveats:         req.Caveats.clone(),
		IssuedAt:        now.Truncate(tokenTimeResolution),
		ExpiresAt:       expires,
		ParentID:        parent.ID,
		Generation:      DefaultGeneration,
		ConfirmationJKT: req.ConfirmationJKT,
	}
	if child.ConfirmationJKT == "" {
		child.ConfirmationJKT = parent.ConfirmationJKT
	}
	// A child starts no earlier than its parent, as it ends no later. The
	// parent may start after now — a not-before ahead, or an issuer replica
	// whose clock runs ahead of this one — and the child then waits for it.
	nbf := req.NotBefore
	childStart := nbf
	if childStart.IsZero() {
		childStart = now.Truncate(tokenTimeResolution) // as IssuedAt records it
	}
	if ps := validFrom(parent); childStart.Before(ps) {
		nbf = ps
	}
	if err := setNotBefore(&child, nbf); err != nil {
		return Capability{}, "", err
	}

	if err := Narrows(parent, child); err != nil {
		return Capability{}, "", fmt.Errorf("limes: delegate %w", err)
	}

	// A delegation is requested by whoever holds the parent — that is not a
	// caller-supplied fact, it is what delegation means, so it is derived
	// rather than accepted as an argument.
	token, err := i.sign(ctx, child)
	if err != nil {
		return Capability{}, "", err
	}
	if err := i.store.Insert(ctx, InsertRequest{Capability: child, IssuedBy: parent.Subject}); err != nil {
		return Capability{}, "", fmt.Errorf("limes: persist delegation: %w", err)
	}
	// The store counts the chain only once the child is on it: a parent
	// at MaxDelegationDepth checks out above, its child does not.
	if _, err := i.store.IsRevoked(ctx, child.ID); err != nil {
		return Capability{}, "", fmt.Errorf("limes: delegate from parent %s: %w", parent.ID, err)
	}
	return child, token, nil
}

// GenerateKey makes a fresh Ed25519 signing key under the kid jwks.KeyID
// derives for it. It lives only as long as the process unless saved.
func GenerateKey() (SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return SigningKey{}, fmt.Errorf("limes: generate key: %w", err)
	}
	return SigningKey{KeyID: jwks.KeyID(pub), Public: pub, Private: priv}, nil
}

// validateThumbprint accepts "" (unbound) or a base64url SHA-256 thumbprint.
// A malformed binding could match no key, minting a capability nobody can use.
func validateThumbprint(jkt string) error {
	if jkt != "" && !dpop.IsThumbprint(jkt) {
		return invalidRequest("ConfirmationJKT %q is not a base64url SHA-256 thumbprint", jkt)
	}
	return nil
}
