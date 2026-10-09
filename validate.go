package limes

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"
)

// ErrInvalidCaveats is returned when a caveat bag is malformed: a negative or
// non-finite budget, an unknown unit, an empty resource entry, an unparsable
// CIDR, an operation name that is not well formed. It is a request error, not
// an authorisation decision — the capability was never minted — so it also
// matches ErrInvalidRequest.
var ErrInvalidCaveats error = &requestError{"capability: invalid caveats"}

// builtinOps is the closed set this package defines. Consumers add their own
// operations as namespaced names ("tool:search", "mcp:github/create_issue");
// see Op.
var builtinOps = map[Op]bool{
	OpGet: true, OpPut: true, OpList: true, OpDelete: true, OpPresign: true,
	OpTag: true, OpSearch: true, OpEmbed: true, OpShare: true, OpManage: true,
}

// readOnlyOps are the built-in operations that do not change state. Anything
// else — including every consumer-defined operation, whose effect this package
// cannot know — is treated as mutating, so a caveat that tightens mutations
// errs towards applying.
var readOnlyOps = map[Op]bool{
	OpGet: true, OpList: true, OpSearch: true, OpEmbed: true, OpPresign: true,
}

// Builtin reports whether op is one of the operations this package defines.
func (op Op) Builtin() bool { return builtinOps[op] }

// Mutating reports whether op may change state when no effect is declared for
// it (see Op.ResolveEffect). Built-in read operations are not mutating; every
// other operation is. OpPresign is read-only by itself — what a presigned URL
// may do is gated by the get/put assertion that accompanies it.
func (op Op) Mutating() bool { return op.defaultEffect() == EffectWrite }

// Validate reports whether op is well formed: a built-in operation, or a
// namespaced one of the form "<namespace>:<name>" made of printable,
// non-space ASCII.
func (op Op) Validate() error {
	if builtinOps[op] {
		return nil
	}
	s := string(op)
	ns, name, ok := strings.Cut(s, ":")
	if !ok || ns == "" || name == "" {
		return fmt.Errorf("%w: unknown op %q (custom ops are namespaced, e.g. \"tool:search\")",
			ErrInvalidCaveats, s)
	}
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%w: op %q contains a non-printable or space character",
				ErrInvalidCaveats, s)
		}
	}
	return nil
}

// Validate reports whether the caveat bag is well formed. The issuer runs it
// before minting anything, root or delegated, so every signed capability
// carries caveats with one meaning.
func (c Caveats) Validate() error {
	if len(c.Ops) == 0 {
		return fmt.Errorf("%w: at least one op required", ErrInvalidCaveats)
	}
	for _, op := range c.Ops {
		if err := op.Validate(); err != nil {
			return err
		}
	}
	for _, p := range c.ResourcePrefixes {
		if err := validResource("resource prefix", p); err != nil {
			return err
		}
	}
	for _, u := range c.ResourceURIs {
		if err := validResource("resource URI", u); err != nil {
			return err
		}
	}
	if c.MaxRequests < 0 {
		return fmt.Errorf("%w: max_requests %d is negative", ErrInvalidCaveats, c.MaxRequests)
	}
	if c.MaxBudgetAmount < 0 || c.MaxBudgetAmount > MaxNanos {
		return fmt.Errorf("%w: max_budget_amount %s is outside 0..%s",
			ErrInvalidCaveats, c.MaxBudgetAmount, MaxNanos)
	}
	if _, err := NormaliseUnitCode(c.UnitCode); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCaveats, err)
	}
	for _, cidr := range c.SourceIPCIDR {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("%w: source_ip_cidr %q: %w", ErrInvalidCaveats, cidr, err)
		}
	}
	return nil
}

func validResource(kind, s string) error {
	if s == "" {
		return fmt.Errorf("%w: empty %s (leave the list empty for \"any resource\")",
			ErrInvalidCaveats, kind)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s %q contains a control character", ErrInvalidCaveats, kind, s)
		}
	}
	return nil
}
