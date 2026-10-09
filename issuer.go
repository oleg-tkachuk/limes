package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Issuer mints capabilities. Two responsibilities, in this order:
//
//  1. Validate the request (caveats sane, parent narrowing if delegated).
//  2. Persist the audit row, then sign and return the token.
//
// The persist-then-sign order is deliberate. If we signed first and the
// DB write failed, an emitted token would have no record — verifiers
// that consult capability_records (admin tooling) would see a forged-
// looking token, and a missing row prevents Revoke from doing anything
// useful. Persist-first means the token in the caller's hand is always
// reflected in storage; a failed sign just means the row is unused
// (cleaned up by housekeeping at the natural expiry).
type Issuer struct {
	signer Signer
	store  Store
	clock  func() time.Time

	issuerName string
	defaultTTL time.Duration
}

// defaultIssueTTL is IssuerConfig.DefaultTTL when unset.
const defaultIssueTTL = 15 * time.Minute

// IssuerConfig is the wiring for Issuer.New.
type IssuerConfig struct {
	// Signer mints tokens. Production wraps a KMS-held private key;
	// dev / tests use NewEd25519Signer with a local generated key.
	Signer Signer

	// Store persists issuance + revocation rows.
	Store Store

	// IssuerName is the value placed in the `iss` claim. Verifiers
	// require this to be in their TrustedIssuers list.
	IssuerName string

	// DefaultTTL caps the lifetime when IssueRequest.TTL is zero.
	// Default 15 minutes when zero — short enough that revocation
	// propagation latency rarely matters.
	DefaultTTL time.Duration

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
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = defaultIssueTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Issuer{
		signer:     cfg.Signer,
		store:      cfg.Store,
		clock:      cfg.Now,
		issuerName: cfg.IssuerName,
		defaultTTL: cfg.DefaultTTL,
	}, nil
}

// IssueRequest is the input shape for Issuer.Issue. All fields except
// TTL / NotBefore / Generation are required; the issuer fills in ID,
// IssuedAt, ExpiresAt, Issuer, Generation when not supplied. Caveats are
// validated (Caveats.Validate) before anything is persisted.
type IssueRequest struct {
	Subject Principal
	// IssuedBy is the principal REQUESTING the capability — an operator for a
	// root issuance, the parent's holder for a delegation. Distinct from
	// Subject, which is who the capability authorises. Required: a capability
	// nobody requested cannot be audited, and the store rejects an empty one.
	IssuedBy   Principal
	Audience   []string
	Caveats    Caveats
	TTL        time.Duration // 0 → DefaultTTL
	NotBefore  time.Time     // zero → now
	Generation int64         // 0 → 1
	// ConfirmationJKT binds the capability to the key with this RFC 7638
	// thumbprint (KeyThumbprint); empty issues a bearer capability.
	ConfirmationJKT string
}

// Issue mints a top-level capability. Generation defaults to 1; see
// Capability.Generation for what it does and does not promise.
//
// Returns the typed Capability and the compact-form token in one call —
// callers usually need both: the token to ship to the agent, the struct
// to log / display.
func (i *Issuer) Issue(ctx context.Context, req IssueRequest) (Capability, string, error) {
	if req.Subject.TenantID == uuid.Nil {
		return Capability{}, "", invalidRequest("Issue requires Subject.TenantID")
	}
	if req.IssuedBy.Subject == "" {
		return Capability{}, "", invalidRequest("IssuedBy is required")
	}
	if req.Generation < 0 {
		return Capability{}, "", invalidRequest("Generation %d is negative", req.Generation)
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
	cap := Capability{
		ID:              uuid.New(),
		Issuer:          i.issuerName,
		Subject:         req.Subject,
		Audience:        req.Audience,
		Caveats:         req.Caveats,
		IssuedAt:        now,
		ExpiresAt:       expires,
		Generation:      req.Generation,
		ConfirmationJKT: req.ConfirmationJKT,
	}
	if cap.Generation == 0 {
		cap.Generation = 1
	}
	if err := setNotBefore(&cap, req.NotBefore); err != nil {
		return Capability{}, "", err
	}

	if err := i.store.Insert(ctx, cap, req.IssuedBy); err != nil {
		return Capability{}, "", fmt.Errorf("limes: persist issuance: %w", err)
	}
	token, err := i.signer.Sign(cap)
	if err != nil {
		return Capability{}, "", fmt.Errorf("limes: sign: %w", err)
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
	return now.Add(ttl), nil
}

// setNotBefore applies a requested not-before time, refusing one at or after
// the expiry: such a capability could never be used.
func setNotBefore(c *Capability, nbf time.Time) error {
	if nbf.IsZero() {
		return nil
	}
	nbf = nbf.UTC()
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
	Parent   Capability
	Subject  Principal
	Audience []string
	Caveats  Caveats
	// InheritCaveats copies the parent's caveats verbatim and ignores
	// Caveats. It must be asked for: an earlier version inherited silently
	// whenever Caveats.Ops was empty, which replaced every other field the
	// caller HAD set — a budget of 2 became the parent's 25 without a word.
	InheritCaveats bool
	TTL            time.Duration
	NotBefore      time.Time
	// ConfirmationJKT binds the child to a key — typically the sub-agent's.
	// Empty keeps the parent's binding, if it has one: a bound parent's
	// child is always bound.
	ConfirmationJKT string
}

// Delegate mints a child capability narrower than the supplied parent.
// The parent must already be a verified Capability — callers run it
// through Verifier.Verify first; the Issuer trusts its in-memory shape.
// It does not trust it to be current, though: a parent that has expired,
// or that the store reports revoked (itself or any ancestor), delegates
// nothing; nor does a Biscuit copy with limits of its own (Parent.Copies),
// which returns ErrDelegationTooWide.
func (i *Issuer) Delegate(ctx context.Context, req DelegateRequest) (Capability, string, error) {
	if req.Parent.ID == uuid.Nil {
		return Capability{}, "", invalidRequest("Delegate requires Parent.ID")
	}
	// A child counts against the capability and its ancestors, never against
	// a Biscuit copy's own counters, so a copy with limits of its own would
	// shed them by delegating. Its holder attenuates it instead.
	if len(req.Parent.Copies) > 0 {
		return Capability{}, "", fmt.Errorf("%w: parent is a Biscuit copy with limits of its own; attenuate it instead",
			ErrDelegationTooWide)
	}
	if req.Subject.TenantID == uuid.Nil {
		req.Subject.TenantID = req.Parent.Subject.TenantID
	}
	if len(req.Audience) == 0 {
		req.Audience = append([]string(nil), req.Parent.Audience...)
	}
	if req.InheritCaveats {
		req.Caveats = req.Parent.Caveats
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
	if !now.Before(req.Parent.ExpiresAt) {
		return Capability{}, "", fmt.Errorf("limes: delegate from parent %s: %w", req.Parent.ID, ErrExpired)
	}
	revoked, err := i.store.IsRevoked(ctx, req.Parent.ID)
	if err != nil {
		return Capability{}, "", fmt.Errorf("limes: delegate revocation lookup: %w", err)
	}
	if revoked {
		return Capability{}, "", fmt.Errorf("limes: delegate from parent %s: %w", req.Parent.ID, ErrRevoked)
	}

	expires, err := i.expiry(now, req.TTL)
	if err != nil {
		return Capability{}, "", err
	}
	if expires.After(req.Parent.ExpiresAt) {
		expires = req.Parent.ExpiresAt
	}

	child := Capability{
		ID:              uuid.New(),
		Issuer:          i.issuerName,
		Subject:         req.Subject,
		Audience:        req.Audience,
		Caveats:         req.Caveats,
		IssuedAt:        now,
		ExpiresAt:       expires,
		ParentID:        req.Parent.ID,
		Generation:      1,
		ConfirmationJKT: req.ConfirmationJKT,
	}
	if child.ConfirmationJKT == "" {
		child.ConfirmationJKT = req.Parent.ConfirmationJKT
	}
	if err := setNotBefore(&child, req.NotBefore); err != nil {
		return Capability{}, "", err
	}

	if err := Narrows(req.Parent, child); err != nil {
		return Capability{}, "", fmt.Errorf("limes: delegate %w", err)
	}

	// A delegation is requested by whoever holds the parent — that is not a
	// caller-supplied fact, it is what delegation means, so it is derived
	// rather than accepted as an argument.
	if err := i.store.Insert(ctx, child, req.Parent.Subject); err != nil {
		return Capability{}, "", fmt.Errorf("limes: persist delegation: %w", err)
	}
	token, err := i.signer.Sign(child)
	if err != nil {
		return Capability{}, "", fmt.Errorf("limes: sign delegation: %w", err)
	}
	return child, token, nil
}

// GenerateEd25519Keypair is a convenience for boot wiring: returns a new
// Ed25519 keypair plus the kid you should associate with it. kid is
// derived from the public key bytes (first 16 hex chars of the raw key)
// so two boot paths producing the same key never end up with different
// kids — useful when a deploy upgrades the persisted key set without
// touching the JWKS doc.
func GenerateEd25519Keypair() (kid string, pub ed25519.PublicKey, priv ed25519.PrivateKey, err error) {
	pub, priv, err = ed25519.GenerateKey(nil)
	if err != nil {
		return "", nil, nil, fmt.Errorf("limes: generate keypair: %w", err)
	}
	kid = fmt.Sprintf("%x", pub[:8])
	return kid, pub, priv, nil
}
