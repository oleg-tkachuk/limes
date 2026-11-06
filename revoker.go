package limes

import (
	"context"
	"log/slog"
	"slices"
	"uuid"
)

// Revoker is the admin plane's way to revoke: one capability, the
// capabilities matching a selector, or one Biscuit copy, each carried out by
// the store and logged — revocation is the audit event that matters most —
// with an event for metrics or an audit sink, and the answers it outdates
// dropped from a RevocationCache in the same process. It holds no signing
// key, so a service that revokes needs none, and stands beside the Issuer
// and the Verifier rather than in either.
//
// Each method returns the store's result and error unchanged. A Revoker is
// safe for concurrent use.
type Revoker struct {
	store    RevocationStore
	bulk     BulkRevoker
	biscuits BiscuitRevocationStore
	cache    *RevocationCache

	logger *slog.Logger
	events RevokerEvents
}

// RevokerConfig is the wiring for NewRevoker.
type RevokerConfig struct {
	// Store revokes one capability. Any Store is one.
	Store RevocationStore

	// Bulk revokes the capabilities matching a selector (RevokeMatching).
	// memstore's and pgstore's Store are one. Nil: RevokeMatching refuses
	// with ErrInvalidConfig.
	Bulk BulkRevoker

	// Biscuits revokes one Biscuit copy (RevokeBiscuit). memstore's and
	// pgstore's Store are one. Nil: RevokeBiscuit refuses with
	// ErrInvalidConfig.
	Biscuits BiscuitRevocationStore

	// Cache is this process's RevocationCache, cleared of what each
	// revocation outdates as soon as the store has carried it out:
	// ClearRevoked of the capability after Revoke, which reaches every
	// descendant the cache holds an answer for; Clear after RevokeMatching,
	// whose result names no capability, and after RevokeBiscuit, as a
	// copy's revocation takes. It reaches no other process's cache: there,
	// a revocation is seen through pgstore.RevocationWatcher or once the
	// cache's TTL runs out. Nil: no cache is cleared.
	Cache *RevocationCache

	// Logger logs every revocation, naming capabilities by id and never
	// logging a token: each call the store carried out at INFO — a repeat
	// that wrote nothing among them — each request it refused at WARN, each
	// call that failed for the server's own reasons at ERROR. Nil:
	// slog.Default().
	Logger *slog.Logger

	// Events are called beside the log, for metrics or an audit sink. The
	// zero RevokerEvents calls nothing.
	Events RevokerEvents
}

// errNoRevokerBulk is RevokeMatching on a revoker given no BulkRevoker.
var errNoRevokerBulk = invalidConfig("limes: RevokerConfig.Bulk required to revoke matching capabilities")

// errNoRevokerBiscuits is RevokeBiscuit on a revoker given no
// BiscuitRevocationStore.
var errNoRevokerBiscuits = invalidConfig("limes: RevokerConfig.Biscuits required to revoke a Biscuit copy")

// NewRevoker validates wiring and returns a ready Revoker. It refuses no
// Store, and a Store, Bulk or Biscuits holding a nil value.
func NewRevoker(cfg RevokerConfig) (*Revoker, error) {
	// An interface holding a nil pointer is not nil: unchecked, it would
	// pass here and panic at the first revocation.
	switch {
	case isNilValue(cfg.Store):
		return nil, invalidConfig("limes: RevokerConfig.Store required")
	case cfg.Bulk != nil && isNilValue(cfg.Bulk):
		return nil, invalidConfig("limes: RevokerConfig.Bulk holds a nil value")
	case cfg.Biscuits != nil && isNilValue(cfg.Biscuits):
		return nil, invalidConfig("limes: RevokerConfig.Biscuits holds a nil value")
	}
	return &Revoker{
		store:    cfg.Store,
		bulk:     cfg.Bulk,
		biscuits: cfg.Biscuits,
		cache:    cfg.Cache,
		logger:   cfg.Logger,
		events:   cfg.Events,
	}, nil
}

// Revoke revokes one capability through the store (RevocationStore.Revoke),
// then drops what that outdates from the cache.
func (r *Revoker) Revoke(ctx context.Context, req RevokeRequest) (RevokeResult, error) {
	attrs := []slog.Attr{
		slog.String(logKeyCapability, req.CapabilityID.String()),
		slog.String(logKeyActor, req.Actor),
		slog.String(logKeyNote, req.Note),
		slog.Bool(logKeyCascade, req.CascadeChildren),
	}
	res, err := r.store.Revoke(ctx, req)
	if err != nil {
		r.refused(ctx, RevokerErrorEvent{Operation: RevokerOperationRevoke, CapabilityID: req.CapabilityID, Err: err}, attrs)
		return res, err
	}
	if r.cache != nil {
		r.cache.ClearRevoked(req.CapabilityID)
	}
	attrs = append(attrs,
		slog.Bool(logKeyAlreadyRevoked, res.AlreadyRevoked),
		slog.String(logKeyPrior, string(res.PriorDisposition)))
	r.revoked(ctx, RevokedEvent{
		Operation: RevokerOperationRevoke, CapabilityID: req.CapabilityID,
		Actor: req.Actor, Note: req.Note,
		AlreadyRevoked: res.AlreadyRevoked, PriorDisposition: res.PriorDisposition,
	}, attrs)
	return res, nil
}

// RevokeMatching revokes a batch of the capabilities matching req through
// the BulkRevoker, then clears the cache. Without one it is ErrInvalidConfig.
func (r *Revoker) RevokeMatching(ctx context.Context, req RevokeMatchingRequest) (RevokeMatchingResult, error) {
	attrs := matchingAttrs(req)
	if r.bulk == nil {
		r.refused(ctx, RevokerErrorEvent{Operation: RevokerOperationRevokeMatching, TenantID: req.TenantID, Err: errNoRevokerBulk}, attrs)
		return RevokeMatchingResult{}, errNoRevokerBulk
	}
	res, err := r.bulk.RevokeMatching(ctx, req)
	if err != nil {
		r.refused(ctx, RevokerErrorEvent{Operation: RevokerOperationRevokeMatching, TenantID: req.TenantID, Err: err}, attrs)
		return res, err
	}
	if r.cache != nil {
		r.cache.Clear()
	}
	tenants := make([]string, len(res.Tenants))
	for i, t := range res.Tenants {
		tenants[i] = t.String()
	}
	attrs = append(attrs,
		slog.Int64(logKeyRevoked, res.Revoked),
		slog.Any(logKeyTenants, tenants),
		slog.Bool(logKeyMore, res.More))
	r.revoked(ctx, RevokedEvent{
		Operation: RevokerOperationRevokeMatching, TenantID: req.TenantID,
		Actor: req.Actor, Note: req.Note,
		Revoked: res.Revoked, Tenants: slices.Clone(res.Tenants), More: res.More,
	}, attrs)
	return res, nil
}

// RevokeBiscuit revokes one Biscuit copy through the BiscuitRevocationStore,
// then clears the cache. Without one it is ErrInvalidConfig. The copy is
// logged by its capability's id alone.
func (r *Revoker) RevokeBiscuit(ctx context.Context, req RevokeBiscuitRequest) (RevokeBiscuitResult, error) {
	id := req.Copy.CapabilityID()
	attrs := []slog.Attr{
		slog.String(logKeyCapability, id.String()),
		slog.String(logKeyActor, req.Actor),
		slog.String(logKeyNote, req.Note),
	}
	if r.biscuits == nil {
		r.refused(ctx, RevokerErrorEvent{Operation: RevokerOperationRevokeBiscuit, CapabilityID: id, Err: errNoRevokerBiscuits}, attrs)
		return RevokeBiscuitResult{}, errNoRevokerBiscuits
	}
	res, err := r.biscuits.RevokeBiscuit(ctx, req)
	if err != nil {
		r.refused(ctx, RevokerErrorEvent{Operation: RevokerOperationRevokeBiscuit, CapabilityID: id, Err: err}, attrs)
		return res, err
	}
	if r.cache != nil {
		r.cache.Clear()
	}
	attrs = append(attrs, slog.Bool(logKeyAlreadyRevoked, res.AlreadyRevoked))
	r.revoked(ctx, RevokedEvent{
		Operation: RevokerOperationRevokeBiscuit, CapabilityID: id,
		Actor: req.Actor, Note: req.Note, AlreadyRevoked: res.AlreadyRevoked,
	}, attrs)
	return res, nil
}

// matchingAttrs name a RevokeMatching request: its tenant, or every tenant,
// each selector it gives, and who asked and why.
func matchingAttrs(req RevokeMatchingRequest) []slog.Attr {
	var attrs []slog.Attr
	if req.AllTenants {
		attrs = append(attrs, slog.Bool(logKeyAllTenants, true))
	} else {
		attrs = append(attrs, slog.String(logKeyTenant, req.TenantID.String()))
	}
	for _, s := range []struct{ key, value string }{
		{logKeySubject, req.Subject},
		{logKeyOnBehalfOf, req.OnBehalfOf},
		{logKeyClient, req.ClientID},
		{logKeySession, req.Session},
		{logKeySubjectIssuer, req.SubjectIssuer},
	} {
		if s.value != "" {
			attrs = append(attrs, slog.String(s.key, s.value))
		}
	}
	if req.RunID != uuid.Nil() {
		attrs = append(attrs, slog.String(logKeyRun, req.RunID.String()))
	}
	return append(attrs, slog.String(logKeyActor, req.Actor), slog.String(logKeyNote, req.Note))
}
