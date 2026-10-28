package limes

import (
	"fmt"
	"net/netip"
	"slices"
	"time"
)

// Narrows reports whether `child` is a narrowing of `parent`: every operation
// child allows is allowed by parent; every resource child can touch is
// reachable by parent; the budget / request count are no larger, and the
// validity window starts no earlier and ends no later;
// the audience is a subset; tainted-read is no broader. Issuer.Delegate runs
// this check before signing the child token, so a misuse fails at issue time,
// not later.
//
// The function is exposed at package level so external tooling can dry-run a
// delegation without going through the issuer.
func Narrows(parent, child Capability) error {
	// Audience: child audience must be subset of parent audience.
	if !subset(child.Audience, parent.Audience) {
		return fmt.Errorf("%w: audience %v widens parent %v",
			ErrDelegationTooWide, child.Audience, parent.Audience)
	}

	// TTL: child cannot outlive parent. We compare ExpiresAt rather
	// than a derived TTL so capabilities issued back-to-back at the
	// same instant don't drift due to clock skew.
	if child.ExpiresAt.After(parent.ExpiresAt) {
		return fmt.Errorf("%w: child expires_at %s outlives parent %s",
			ErrDelegationTooWide, child.ExpiresAt, parent.ExpiresAt)
	}

	// Start: child cannot begin before parent. A capability with no
	// NotBefore starts when it was issued; one with neither, at any time.
	// Compared exactly: a token's times are whole seconds, so a parent read
	// back from its token compares equal to itself, and rounding a parent's
	// start down would let a child begin up to a second before it.
	if ps := validFrom(parent); !ps.IsZero() && validFrom(child).Before(ps) {
		return fmt.Errorf("%w: child starts %s, before parent %s",
			ErrDelegationTooWide, validFrom(child), ps)
	}

	// Tenant: must match. Cross-tenant delegation is impossible — a
	// capability is forever pinned to its issuing tenant.
	if child.Subject.TenantID != parent.Subject.TenantID {
		return fmt.Errorf("%w: tenant_id mismatch", ErrDelegationTooWide)
	}

	// Key binding: a bound parent cannot delegate an unbound child — that
	// would turn a token useless without its key into one any holder can
	// replay. The child may be bound to a different key: the sub-agent's.
	if parent.ConfirmationJKT != "" && child.ConfirmationJKT == "" {
		return fmt.Errorf("%w: parent is key-bound, child is not", ErrDelegationTooWide)
	}

	return narrowsCaveats(parent.Caveats, child.Caveats)
}

// validFrom is when c starts to be valid: its NotBefore, or its IssuedAt
// without one. Zero for neither — valid from any time.
func validFrom(c Capability) time.Time {
	if !c.NotBefore.IsZero() {
		return c.NotBefore
	}
	return c.IssuedAt
}

// narrowsCaveats compares the typed caveat bag. Every restriction in
// the child must be at-least-as-strict as the parent.
func narrowsCaveats(parent, child Caveats) error {
	if err := narrowsOps(parent.Ops, child.Ops); err != nil {
		return err
	}
	if err := narrowsResources(parent, child); err != nil {
		return err
	}
	if err := narrowsLimits(parent, child); err != nil {
		return err
	}
	if err := narrowsFlags(parent, child); err != nil {
		return err
	}
	return narrowsSourceIP(parent.SourceIPCIDR, child.SourceIPCIDR)
}

// narrowsOps requires the child's ops to be a subset of the parent's.
func narrowsOps(parent, child []Op) error {
	for _, op := range child {
		if !slices.Contains(parent, op) {
			return fmt.Errorf("%w: child op %q not in parent ops %v",
				ErrDelegationTooWide, op, parent)
		}
	}
	return nil
}

// narrowsLimits requires each of the child's ceilings — requests, copies,
// budget — within the parent's, and its budget in the parent's unit.
func narrowsLimits(parent, child Caveats) error {
	// Requests: a child's ceiling may not lift or raise its parent's.
	if !child.Requests.Within(parent.Requests) {
		return fmt.Errorf("%w: child requests %s would exceed parent %s",
			ErrDelegationTooWide, child.Requests, parent.Requests)
	}

	// Copies: a child counts no more Biscuit copies than its parent.
	if !child.Copies.Within(parent.Copies) {
		return fmt.Errorf("%w: child copies %s would exceed parent %s",
			ErrDelegationTooWide, child.Copies, parent.Copies)
	}

	// Unit code: a child budget counts in the parent's unit. We don't
	// auto-convert between currencies — a delegated child in EUR off a
	// USD parent is a configuration mistake (which budget does the
	// eventual charge land on?) and is rejected at issuance. Only a child
	// budget that spends nothing may name no unit; Issuer.Delegate gives
	// it its parent's.
	if child.Budget.UnitCode != parent.Budget.UnitCode && (child.Budget.UnitCode != "" || child.Budget.permitsSpend()) {
		return fmt.Errorf("%w: parent unit_code %q vs child %q",
			ErrUnitCodeMismatch, parent.Budget.UnitCode, child.Budget.UnitCode)
	}

	// Budget: same rule. Compared in the shared unit_code (validated
	// just above) so we don't need an FX rate.
	if !child.Budget.Within(parent.Budget) {
		return fmt.Errorf("%w: child budget %s exceeds parent %s",
			ErrDelegationTooWide, child.Budget, parent.Budget)
	}
	return nil
}

// narrowsFlags requires the child to grant no privilege the parent lacks
// and to relax no requirement the parent sets.
func narrowsFlags(parent, child Caveats) error {
	// AllowTaintedRead: child cannot grant a privilege the parent lacks.
	if child.AllowTaintedRead && !parent.AllowTaintedRead {
		return fmt.Errorf("%w: child grants tainted_read; parent does not",
			ErrDelegationTooWide)
	}

	// IdempotencyKeyRequired: enforcement-tightening only — child may
	// raise the bar but not lower it.
	if parent.IdempotencyKeyRequired && !child.IdempotencyKeyRequired {
		return fmt.Errorf("%w: child relaxes idempotency_key requirement",
			ErrDelegationTooWide)
	}
	return nil
}

// narrowsResources requires every resource the child can reach to be
// reachable by the parent. A parent that restricts resources in ANY form
// forces the child to restrict too, and each child entry is checked against
// the whole parent allowance:
//
//   - a child URI must equal a parent URI or sit under a parent prefix;
//   - a child prefix must sit under a parent prefix. A parent that lists only
//     exact URIs admits no child prefix at all — a prefix names an open-ended
//     set, which no finite list of URIs covers.
//
// The second rule holds whether or not the parent has prefixes: a parent
// pinned to a single URI admits no child prefix.
func narrowsResources(parent, child Caveats) error {
	if !parent.RestrictsResources() {
		return nil
	}
	if !child.RestrictsResources() {
		return fmt.Errorf("%w: parent restricts resources (prefixes %v, URIs %v), child unrestricted",
			ErrDelegationTooWide, parent.ResourcePrefixes, parent.ResourceURIs)
	}
	for _, c := range child.ResourcePrefixes {
		if !parent.coversPrefix(c) {
			return fmt.Errorf("%w: child prefix %q not under any parent prefix %v",
				ErrDelegationTooWide, c, parent.ResourcePrefixes)
		}
	}
	for _, c := range child.ResourceNamePrefixes {
		if !parent.coversNamePrefix(c) {
			return fmt.Errorf("%w: child name prefix %q not within the parent's prefixes %v or name prefixes %v",
				ErrDelegationTooWide, c, parent.ResourcePrefixes, parent.ResourceNamePrefixes)
		}
	}
	for _, c := range child.ResourceURIs {
		if !parent.AllowsResource(c) {
			return fmt.Errorf("%w: child URI %q not in parent allowance",
				ErrDelegationTooWide, c)
		}
	}
	return nil
}

// narrowsSourceIP requires every child network to lie inside some parent
// network. A non-empty parent forces the child to constrain as well.
// Unparsable entries never satisfy the check — Validate rejects them at
// issuance, and a parent minted before validation existed must not let a
// malformed entry wave a child through.
func narrowsSourceIP(parent, child []string) error {
	if len(parent) == 0 {
		return nil
	}
	if len(child) == 0 {
		return fmt.Errorf("%w: parent restricts source IP, child unrestricted",
			ErrDelegationTooWide)
	}
	for _, c := range child {
		cp, err := netip.ParsePrefix(c)
		if err != nil {
			return fmt.Errorf("%w: child CIDR %q: %w", ErrDelegationTooWide, c, err)
		}
		if !slices.ContainsFunc(parent, func(p string) bool {
			pp, err := netip.ParsePrefix(p)
			return err == nil && prefixContains(pp, cp)
		}) {
			return fmt.Errorf("%w: child CIDR %q not inside any parent CIDR %v",
				ErrDelegationTooWide, c, parent)
		}
	}
	return nil
}

// prefixContains reports whether network inner lies entirely within outer.
func prefixContains(outer, inner netip.Prefix) bool {
	outer, inner = outer.Masked(), inner.Masked()
	return outer.Addr().Is4() == inner.Addr().Is4() &&
		outer.Bits() <= inner.Bits() &&
		outer.Contains(inner.Addr())
}

// subset reports whether every element of a is in b. Empty a is a
// subset of any b (nothing-claim-anything degenerates safely).
func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}
