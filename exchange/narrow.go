package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
)

// NarrowRequest is an exchange of a capability for a narrower one, for a
// sub-agent: the capability is the subject token (RFC 8693 Â§2.1), of type
// IssuedTokenTypeCapability.
type NarrowRequest struct {
	// Client is the client asking; its ID is required, for the log and as
	// its share of the DPoP replay cache.
	Client Client
	// SubjectToken is the capability narrowed. Required.
	SubjectToken string
	// Resources and Audiences are what the child is asked for; at least one
	// of either, each within the parent's audience.
	Resources, Audiences []string
	// Scopes and AuthorizationDetails are handed to the policy.
	Scopes               []string
	AuthorizationDetails json.RawMessage
	// DPoP is the request's proof. For a parent bound to a key it is the
	// proof of holding it, over the subject token, and the child keeps the
	// binding; for an unbound parent, a token request's proof (RFC 9449 Â§5)
	// binds the child to its key, and none leaves the child a bearer one.
	DPoP dpop.Proof
}

// Narrowing is what a NarrowPolicy decides on: the verified parent, its
// client, and what is asked for.
type Narrowing struct {
	Parent               limes.VerifiedCapability
	Client               Client
	Resources, Audiences []string
	Scopes               []string
	AuthorizationDetails json.RawMessage
}

// NarrowPolicy turns a narrowing into the child to delegate, or refuses it
// with ErrInvalidTarget or ErrInvalidScope. Its DelegateRequest's Parent is
// set by the Exchanger; its Audience must be drawn from the narrowing's
// resources and audiences, or be empty to take them all; its
// ConfirmationJKT must be empty. Issuer.Delegate holds the child to the
// parent: narrower caveats, an audience within the parent's, an end no
// later.
type NarrowPolicy func(ctx context.Context, n Narrowing) (limes.DelegateRequest, error)

// errNoNarrowing is an Exchanger configured to exchange no capabilities.
var errNoNarrowing = fmt.Errorf("%w: this exchange narrows no capabilities", ErrInvalidRequest)

// Narrow exchanges a capability for a narrower one, delegated from it: it
// verifies the capability as one addressed to the exchange, checks the
// proof of possession, asks the policy, holds its answer to the request,
// and delegates. A refusal is one of the errors above, or a dpop error for
// the proof; every narrowing and refusal is logged.
//
// A bearer capability is narrowed for whichever authenticated client
// presents it, as it would authorize whoever presents it: unlike Exchange,
// Narrow does not hold the subject to the client, and Narrowing.Client is
// the policy's to compare with the parent's ClientID where only the client
// it was granted to may narrow it. A bound capability is narrowed only with
// a proof by its key. The child keeps the parent's ClientID, the client the
// grant began with, unless the policy names another.
func (e *Exchanger) Narrow(ctx context.Context, req NarrowRequest) (Result, error) {
	res, parent, err := e.narrow(ctx, req)
	log := e.cfg.Logger.With(slog.String("client", req.Client.ID))
	if parent.ID != uuid.Nil() {
		log = log.With(slog.String("parent", parent.ID.String()))
	}
	if err != nil {
		level := slog.LevelWarn
		if !isRefusal(err) {
			level = slog.LevelError
		}
		log.Log(ctx, level, "narrowing refused", slog.Any("error", err))
		return Result{}, err
	}
	log.InfoContext(ctx, "narrowing granted",
		slog.String("capability", res.Capability.ID.String()),
		slog.Any("audience", res.Capability.Audience),
		slog.String("token_type", res.TokenType),
		slog.Time("expires_at", res.Capability.ExpiresAt))
	return res, nil
}

// narrow is Narrow, and the parent once verified, for the log.
func (e *Exchanger) narrow(ctx context.Context, req NarrowRequest) (Result, limes.Capability, error) {
	if err := e.checkNarrowRequest(req); err != nil {
		return Result{}, limes.Capability{}, err
	}
	verified, err := e.verifyParent(ctx, req)
	if err != nil {
		return Result{}, limes.Capability{}, err
	}
	parent := verified.Capability()
	targets := targetsOf(req.Resources, req.Audiences)
	if err := withinParent(targets, parent.Audience); err != nil {
		return Result{}, parent, err
	}
	checked, rebind, err := e.checkRebind(ctx, req, parent)
	if err != nil {
		return Result{}, parent, err
	}
	child, err := e.child(ctx, req, verified, targets)
	if err != nil {
		return Result{}, parent, err
	}
	if rebind {
		// Recorded before the child exists, as for an exchange.
		if err := e.cfg.DPoP.Record(ctx, checked); err != nil {
			return Result{}, parent, err
		}
		child.ConfirmationJKT = checked.JKT()
	}
	minted, err := e.cfg.Issuer.Delegate(ctx, child)
	if err != nil {
		return Result{}, parent, delegateError(err)
	}
	return resultOf(minted), parent, nil
}

// checkNarrowRequest refuses a narrowing this exchanger does not do, or one
// without a client, a subject token or a target.
func (e *Exchanger) checkNarrowRequest(req NarrowRequest) error {
	switch {
	case e.cfg.Capabilities == nil:
		return errNoNarrowing
	case req.Client.ID == "":
		return fmt.Errorf("%w: a client with an id", ErrInvalidClient)
	case req.SubjectToken == "":
		return fmt.Errorf("%w: subject_token required", ErrInvalidRequest)
	}
	return requireTarget(req.Resources, req.Audiences)
}

// verifyParent verifies the capability narrowed. The proof is the parent's
// only when the parent is bound; Verify ignores it otherwise, and it is read
// again as a token request's.
func (e *Exchanger) verifyParent(ctx context.Context, req NarrowRequest) (limes.VerifiedCapability, error) {
	verified, err := e.cfg.Capabilities.Verify(ctx, limes.VerifyRequest{
		Token: req.SubjectToken, Audience: e.cfg.CapabilityAudience, DPoP: req.DPoP,
	})
	switch {
	case err == nil, isRefusal(err):
		return verified, err
	case errors.Is(err, limes.ErrUnauthenticated):
		return verified, fmt.Errorf("%w: %w", ErrInvalidGrant, err)
	}
	return verified, fmt.Errorf("exchange: verify subject capability: %w", err)
}

// withinParent refuses a target outside the parent's audience.
func withinParent(targets, audience []string) error {
	for _, aud := range targets {
		if !slices.Contains(audience, aud) {
			return fmt.Errorf("%w: %q is outside the parent's audience", ErrInvalidTarget, aud)
		}
	}
	return nil
}

// checkRebind checks the proof of a request binding an unbound parent's
// child to a key, as a token request's; rebind reports whether it does.
func (e *Exchanger) checkRebind(ctx context.Context, req NarrowRequest, parent limes.Capability) (checked dpop.CheckedProof, rebind bool, err error) {
	if parent.ConfirmationJKT != "" || req.DPoP.JWT == "" {
		return dpop.CheckedProof{}, false, nil
	}
	if e.cfg.DPoP == nil {
		return dpop.CheckedProof{}, false, fmt.Errorf("%w: this endpoint binds no capability to a key", dpop.ErrInvalid)
	}
	checked, err = e.cfg.DPoP.CheckTokenRequest(ctx, dpop.TokenRequest{Proof: req.DPoP, Holder: req.Client.ID})
	if err != nil {
		return dpop.CheckedProof{}, false, err
	}
	return checked, true, nil
}

// child is the delegation the narrowing policy asks of verified, held to
// the request's targets.
func (e *Exchanger) child(ctx context.Context, req NarrowRequest, verified limes.VerifiedCapability, targets []string) (limes.DelegateRequest, error) {
	child, err := e.cfg.NarrowPolicy(ctx, Narrowing{
		Parent: verified, Client: req.Client, Resources: slices.Clone(req.Resources), Audiences: slices.Clone(req.Audiences),
		Scopes: slices.Clone(req.Scopes), AuthorizationDetails: slices.Clone(req.AuthorizationDetails),
	})
	if err != nil {
		return limes.DelegateRequest{}, err
	}
	if child.Audience, err = holdPolicy(child.Audience, child.ConfirmationJKT, targets); err != nil {
		return limes.DelegateRequest{}, err
	}
	child.Parent = verified
	return child, nil
}

// delegateError is a Delegate failure as the exchange answers it: a parent
// that may delegate nothing more (gone, too deep, or granted without
// OpShare) is the subject token's fault; anything else is the server's.
func delegateError(err error) error {
	if errors.Is(err, limes.ErrUnauthenticated) || errors.Is(err, limes.ErrCaveatViolation) {
		return fmt.Errorf("%w: %w", ErrInvalidGrant, err)
	}
	return fmt.Errorf("exchange: delegate: %w", err)
}
