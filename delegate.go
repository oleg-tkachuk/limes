package limes

import (
	"fmt"
	"net/netip"
	"slices"
)

// Narrows reports whether `child` is a narrowing of `parent`: every operation
// child allows is allowed by parent; every resource child can touch is
// reachable by parent; the budget / request count / lifetime are no larger;
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

// narrowsCaveats compares the typed caveat bag. Every restriction in
// the child must be at-least-as-strict as the parent.
func narrowsCaveats(parent, child Caveats) error {
	// Ops: child set ⊆ parent set.
	for _, op := range child.Ops {
		if !slices.Contains(parent.Ops, op) {
			return fmt.Errorf("%w: child op %q not in parent ops %v",
				ErrDelegationTooWide, op, parent.Ops)
		}
	}

	if err := narrowsResources(parent, child); err != nil {
		return err
	}

	// MaxRequests: 0 = unlimited. Child 0 with parent>0 is invalid
	// (child would have unlimited where parent is bounded).
	if parent.MaxRequests > 0 {
		if child.MaxRequests == 0 || child.MaxRequests > parent.MaxRequests {
			return fmt.Errorf("%w: child requests %d would exceed parent %d",
				ErrDelegationTooWide, child.MaxRequests, parent.MaxRequests)
		}
	}

	// Unit code: child must declare the same unit as the parent.
	// We don't auto-convert between currencies — a delegated child
	// in EUR off a USD parent is a configuration mistake (which
	// budget does the eventual charge land on?) and is rejected at
	// issuance. Both are compared as NormaliseUnitCode reads them, so
	// a parent with no explicit unit does not reject every new child.
	parentUnit, childUnit := canonicalUnitCode(parent.UnitCode), canonicalUnitCode(child.UnitCode)
	if parentUnit != childUnit {
		return fmt.Errorf("%w: parent unit_code %q vs child %q",
			ErrUnitCodeMismatch, parentUnit, childUnit)
	}

	// Budget: same rule. Compared in the shared unit_code (validated
	// just above) so we don't need an FX rate.
	if parent.MaxBudgetAmount > 0 {
		if child.MaxBudgetAmount <= 0 || child.MaxBudgetAmount > parent.MaxBudgetAmount {
			return fmt.Errorf("%w: child budget %s exceeds parent %s",
				ErrDelegationTooWide, child.MaxBudgetAmount, parent.MaxBudgetAmount)
		}
	}

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

	return narrowsSourceIP(parent.SourceIPCIDR, child.SourceIPCIDR)
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
// The second rule is the one an earlier version missed: it checked child
// prefixes only when the parent itself had prefixes, so a parent pinned to a
// single URI could delegate a child with any prefix it liked.
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
