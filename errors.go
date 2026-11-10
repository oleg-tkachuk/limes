package limes

import (
	"errors"

	"github.com/oleg-tkachuk/limes/internal/detail"
	"github.com/oleg-tkachuk/limes/internal/errs"
)

// Errors exposed to callers, which switch on them with errors.Is. A
// consumer maps them to its transport, or lets Verifier.ErrorResponse
// map them to HTTP.
//
// Some are families that more specific sentinels match as well, so a caller
// branches on the one it needs:
//
//	ErrUnauthenticated       the presented credentials were refused (401)
//	├── ErrNoCredentials     none were presented
//	├── ErrInvalidToken      the token is not genuine, or not this verifier's
//	│   └── ErrBiscuitAttenuation
//	├── ErrProofOfPossession the proof of possession is missing or wrong
//	│   └── ErrAuthSchemeMismatch
//	├── ErrRevoked
//	│   └── ErrDelegationTooDeep, ErrTenantErased
//	├── ErrUnrecorded        no record (unless VerifierConfig.AllowUnrecorded)
//	└── ErrExpired, ErrNotYetValid, ErrAudienceMismatch
//	ErrCaveatViolation       a caveat refuses the operation (403)
//	ErrBudgetExceeded, ErrTenantBudgetExceeded, ErrRequestLimitExceeded
//	                         a ceiling refuses the request (403)
//	ErrInvalidRequest        the request itself is malformed
//	├── ErrInvalidCaveats, ErrInvalidAmount, ErrEffectConflict
//	└── ErrMalformedAuthorization  the client's Authorization header (400)
//	ErrDelegationTooWide, ErrDelegationDepthExceeded
//	                         a delegation its parent cannot grant (403)
//	ErrNotFound              what the request names is not on record
//	└── meter.ErrChargeNotFound, meter.ErrUsageNotFound, …
//	ErrUnknownTenant, ErrTenantDeleted, ErrTenantGone
//	                         an issuance for a tenant not open for one
//	ErrConflict              a well-formed request disagrees with what is on record
//	└── ErrAlreadyExists, ErrUnitCodeMismatch, meter.ErrExternalRefConflict, …
var (
	// ErrUnauthenticated is the family of every refusal of the presented
	// credentials, token or proof: the one errors.Is a consumer needs to
	// answer them all as unauthenticated.
	ErrUnauthenticated = errors.New("limes: unauthenticated")
	// ErrInvalidToken — the token is not genuine or not this verifier's
	// to accept: a bad signature, an unknown kid, an untrusted issuer, a
	// malformed header or claims. One sentinel for all, so a caller cannot
	// tell an attacker which check failed.
	ErrInvalidToken error = &familyError{"limes: invalid token", ErrUnauthenticated}
	// ErrProofOfPossession is the family of every proof-of-possession
	// refusal: the token may be genuine, the proof that its holder sent it
	// is what failed. The verifier wraps each dpop refusal in it.
	ErrProofOfPossession error = &familyError{"limes: proof of possession refused", ErrUnauthenticated}
	ErrExpired           error = &familyError{"limes: expired", ErrUnauthenticated}
	ErrNotYetValid       error = &familyError{"limes: not yet valid", ErrUnauthenticated}
	ErrRevoked           error = &familyError{"limes: revoked", ErrUnauthenticated}
	// ErrTenantErased is a metering call, or a Store.Insert, for a tenant
	// whose usage EraseTenantUsage removed: nothing of it is recorded or
	// counted again. Its capabilities were revoked with it, so it matches
	// ErrRevoked. The Issuer answers a mint the store refuses with it as
	// ErrTenantGone instead: no presented capability was refused.
	ErrTenantErased error = &familyError{"limes: tenant erased", ErrRevoked}
	// ErrDelegationTooDeep — a capability with more than MaxDelegationDepth
	// ancestors on record. Its root lies past the bounded IsRevoked walk,
	// so a revocation there would go unseen: it is refused as revoked,
	// never accepted. A store answers it as an error, which no revocation
	// cache keeps. Store.Insert refuses to record one with it, which
	// Delegate answers as ErrDelegationDepthExceeded: the parent is live,
	// only too deep to delegate from.
	ErrDelegationTooDeep error = &familyError{"limes: delegation chain too deep", ErrRevoked}
	ErrAudienceMismatch  error = &familyError{"limes: audience mismatch", ErrUnauthenticated}
	// ErrUnrecorded — a capability the revocation lookup has no record of,
	// refused unless VerifierConfig.AllowUnrecorded is set, rather than read as not
	// revoked: a lookup that cannot see the record could not see its
	// revocation either. It is not ErrRevoked, so an operator tells a
	// lookup blind to an issuer's records, or a store restored from an older
	// backup, from a capability someone stopped.
	ErrUnrecorded      error = &familyError{"limes: capability not on record", ErrUnauthenticated}
	ErrCaveatViolation       = errors.New("limes: caveat violation")
	ErrBudgetExceeded        = errors.New("limes: budget exceeded")

	// ErrInvalidAmount — an amount that is negative or above MaxNanos, or a
	// decimal that is not one (ParseAmount). Refunds are explicit; a
	// negative charge is not one. A malformed request, so it matches
	// ErrInvalidRequest.
	ErrInvalidAmount error = &familyError{"limes: invalid amount", ErrInvalidRequest}

	// ErrCursorMismatch — a cursor RecordLister.ListRecords handed out for
	// another order, tenant or other filters, handed back with these: well formed,
	// but a place in another listing. A caller that tells it from a
	// malformed request starts again from the first page. It matches
	// ErrInvalidRequest, as every refused request does; a cursor that is
	// not one at all is ErrInvalidRequest alone.
	ErrCursorMismatch error = &familyError{"limes: cursor of another listing", ErrInvalidRequest}

	// ErrRequestLimitExceeded — the capability, a Biscuit copy or an
	// ancestor used every request its ceiling allows; the next was blocked.
	ErrRequestLimitExceeded = errors.New("limes: request limit exceeded")

	// ErrTenantBudgetExceeded — the tenant aggregate cap has been
	// hit. Distinct from ErrBudgetExceeded because the remedy differs:
	// an operator raises a tenant's ceiling with SetTenantBudget, while a
	// capability's ceiling is in its token and lifting it means issuing a
	// new one.
	ErrTenantBudgetExceeded = errors.New("limes: tenant aggregate budget exceeded")

	ErrDelegationTooWide = errors.New("limes: child wider than parent")
	// ErrDelegationDepthExceeded — a Delegate whose child would have more
	// than MaxDelegationDepth ancestors. The parent stays valid; it is too
	// deep in its chain to delegate from, so the delegation is refused
	// (403) and nothing is recorded. It does not match ErrRevoked or
	// ErrDelegationTooDeep, which are the verify path's answers to a
	// lineage already too deep.
	ErrDelegationDepthExceeded = errors.New("limes: delegation would exceed the maximum depth")
	// ErrUnitCodeMismatch — two units that would have to be one: a
	// delegated child declaring a unit_code other than its parent's, or an
	// amount in a unit other than the one a counter (a capability's, an
	// ancestor's, a tenant's) already counts in. Nothing converts between
	// currencies, so cross-currency delegation is refused at issuance and a
	// cross-currency charge at the Meter. The request is well formed and
	// disagrees with what is on record, so it matches ErrConflict.
	ErrUnitCodeMismatch error = &familyError{"limes: unit_code mismatch", ErrConflict}
	// ErrInvalidRequest is a request malformed in itself: a missing tenant,
	// an empty audience entry, a negative TTL, a thumbprint that is not one,
	// a not-before past the expiry, caveats or an amount that do not
	// validate. Nothing was done. Every such error matches it, so a caller
	// maps the whole family to its "bad request" without listing it. A
	// request that is well formed but asks for more than is allowed has its
	// own sentinel instead: ErrDelegationTooWide, ErrUnitCodeMismatch.
	ErrInvalidRequest = errs.ErrInvalidRequest
	// ErrInvalidConfig is a component wired wrong: a constructor given a
	// config it cannot work with (a required field missing, a negative
	// bound), a key of another kind, or a dependency missing. Nothing was
	// built or done. It is the deployment's to fix, not the caller's, and is
	// answered 500 (docs/errors.md).
	ErrInvalidConfig = errs.ErrInvalidConfig
	// ErrInternal is an invariant of the module broken: a signature its own
	// key does not verify, a store answering otherwise than its contract
	// says. It is answered 500, the error's text, for the server's log,
	// naming what broke.
	ErrInternal = errs.ErrInternal
	// ErrUnknownTenant is an issuance for a tenant that does not exist —
	// typically one whose provisioning has not finished. The Store returns it.
	ErrUnknownTenant = errors.New("limes: unknown tenant")
	// ErrTenantDeleted is an issuance for a tenant in the trash: it exists,
	// so its rows may be restored, but nothing new is minted for it. The
	// Store returns it.
	ErrTenantDeleted = errors.New("limes: tenant deleted")
	// ErrTenantGone is an issuance for a tenant whose usage EraseTenantUsage
	// removed: nothing new is minted for it, and unlike ErrTenantDeleted
	// nothing of it can be restored (410). The Issuer returns it where the
	// Store refuses the record with ErrTenantErased, which it does not
	// match: that sentinel is a revoked capability's, and a mint presents
	// none.
	ErrTenantGone = errors.New("limes: tenant gone")
	// ErrScopeMismatch is a store call whose context is scoped to a tenant
	// other than the one its request names — or to none — under a store
	// that keeps each tenant's rows apart. The rows are there; the call may
	// not see them. It is the caller's wiring that is wrong, not the
	// request, so it matches neither ErrInvalidRequest nor ErrNotFound, and
	// ErrorResponse answers it 500.
	ErrScopeMismatch = errors.New("limes: call scoped to another tenant than its request names")
	// ErrProofUnsupported is a key-bound capability presented to a verifier
	// with no DPoP configured: no proof the client sends could be checked.
	// It is the deployment's fault, not the request's, so it matches no
	// ErrUnauthenticated sentinel and ErrorResponse invites no retry.
	ErrProofUnsupported = errors.New("limes: key-bound capability, and this verifier checks no proof")
	// ErrRevocationUnavailable is a revocation lookup that failed — the
	// store or the upstream that answers it unreachable, or failing — so
	// whether the capability is revoked is unknown. The verifier and the
	// issuer refuse with it, wrapping the lookup's own error, as the
	// verifier refuses a token whose issuer's keys cannot be fetched
	// (jwks.ErrUnavailable): a dependency failed, not the token, so it
	// matches no ErrUnauthenticated sentinel, and is answered 503, to retry.
	// A lookup's error that is a code of limes's already — a lineage too
	// deep (ErrDelegationTooDeep), a store's broken invariant
	// (ErrInternal) — keeps that code.
	ErrRevocationUnavailable = errors.New("limes: revocation status unavailable")
)

// familyError is a sentinel with a name of its own that also matches the
// family it belongs to, and that family's own: errors.Is(dpop.ErrReplayed,
// ErrProofOfPossession) and errors.Is(dpop.ErrReplayed, ErrUnauthenticated)
// both hold.
type familyError struct {
	msg    string
	family error
}

func (e *familyError) Error() string { return e.msg }
func (e *familyError) Is(target error) bool {
	return target == e.family || errors.Is(e.family, target)
}

// invalidRequest is an ErrInvalidRequest saying what was wrong.
func invalidRequest(format string, args ...any) error { return errs.Request(format, args...) }

// refuse is family, saying what format and args make, carrying text: what
// a client may be shown of it (internal/detail, ProblemOf).
func refuse(family error, text detail.Text, format string, args ...any) error {
	return detail.Wrap(errs.Errorf("%w: "+format, append([]any{family}, args...)...), text)
}

// refuseRequest is an ErrInvalidRequest whose text, a constant, is all it
// says, and so what a client may be shown of it as well.
func refuseRequest(text detail.Text) error {
	return detail.Wrap(errs.Request("%s", text), text)
}

// invalidConfig is an ErrInvalidConfig saying what was wrong.
func invalidConfig(format string, args ...any) error { return errs.Config(format, args...) }

// broken is an ErrInternal saying which invariant broke.
func broken(format string, args ...any) error { return errs.Broken(format, args...) }
