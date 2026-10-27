package limes

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"
)

// ErrInvalidCaveats is returned when a caveat bag is malformed: a budget
// outside 0..MaxNanos, a request limit outside 0..MaxIJSONInteger, an unknown unit, an empty resource entry, an unparsable
// CIDR, an operation name that is not well formed. It is a request error, not
// an authorisation decision — the capability was never minted — so it also
// matches ErrInvalidRequest.
var ErrInvalidCaveats error = &familyError{"limes: invalid caveats", ErrInvalidRequest}

// builtinOpList is the closed set this package defines, in the order an
// error lists it. Consumers add their own operations as namespaced names
// ("tool:search", "mcp:github/create_issue"); see Op.
var builtinOpList = []Op{OpGet, OpPut, OpList, OpDelete, OpShare, OpManage}

// builtinOps is builtinOpList as a set.
var builtinOps = func() map[Op]bool {
	set := make(map[Op]bool, len(builtinOpList))
	for _, op := range builtinOpList {
		set[op] = true
	}
	return set
}()

// builtinOpNames is builtinOpList as an error names it.
var builtinOpNames = func() string {
	names := make([]string, len(builtinOpList))
	for i, op := range builtinOpList {
		names[i] = string(op)
	}
	return strings.Join(names, ", ")
}()

// readOnlyOps are the built-in operations that do not change state. Anything
// else — including every consumer-defined operation, whose effect this package
// cannot know — is treated as mutating, so a caveat that tightens mutations
// errs towards applying.
var readOnlyOps = map[Op]bool{OpGet: true, OpList: true}

// Builtin reports whether op is one of the operations this package defines.
func (op Op) Builtin() bool { return builtinOps[op] }

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
		return fmt.Errorf("%w: unknown op %q: the built-in ops are %s; a custom op is namespaced, e.g. \"tool:search\"",
			ErrInvalidCaveats, s, builtinOpNames)
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
	for _, n := range c.ResourceNamePrefixes {
		if err := validResource("resource name prefix", n); err != nil {
			return err
		}
	}
	if err := c.Requests.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCaveats, err)
	}
	if err := c.Budget.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCaveats, err)
	}
	if err := c.Copies.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCaveats, err)
	}
	for _, cidr := range c.SourceIPCIDR {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fmt.Errorf("%w: source_ip_cidr %q: %w", ErrInvalidCaveats, cidr, err)
		}
		// CheckSource unmaps the client's address, so an IPv4-mapped IPv6
		// prefix would admit no client at all: write it as IPv4.
		if p.Addr().Is4In6() {
			return fmt.Errorf("%w: source_ip_cidr %q is IPv4-mapped IPv6; write it as IPv4", ErrInvalidCaveats, cidr)
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
	if hasDotSegment(s) {
		return fmt.Errorf("%w: %s %q has a \".\" or \"..\" segment", ErrInvalidCaveats, kind, s)
	}
	return nil
}

// Validate reports whether p names a principal a capability can be issued
// to: one of the principal kinds, spelled as the constants spell it, and
// agent details only on an agent. The kind is part of the token, so a kind
// outside the set would be signed and then read by every verifier as it
// pleased.
func (p Principal) Validate() error {
	if err := p.Type.Validate(); err != nil {
		return err
	}
	if p.Agent != nil && p.Type != PrincipalTypeAgent {
		return invalidRequest("principal of type %q carries agent details", p.Type)
	}
	return nil
}

// validateOnBehalfOf checks the principal a capability of holder acts on
// behalf of: a principal of holder's tenant, which a capability is pinned
// to, named by a subject.
func validateOnBehalfOf(obo, holder Principal) error {
	if err := obo.Validate(); err != nil {
		return err
	}
	if obo.Subject == "" {
		return invalidRequest("OnBehalfOf names no subject")
	}
	if obo.TenantID != holder.TenantID {
		return invalidRequest("OnBehalfOf is of tenant %s, not the subject's %s", obo.TenantID, holder.TenantID)
	}
	return nil
}

// Validate reports whether t is one of the principal kinds, spelled as the
// constants spell it. Every error matches ErrInvalidRequest.
func (t PrincipalType) Validate() error {
	switch t {
	case PrincipalTypeUser, PrincipalTypeAgent, PrincipalTypeService:
		return nil
	case PrincipalTypeUnspecified:
	}
	return invalidRequest("principal type %q is not one of %q, %q, %q",
		t, PrincipalTypeUser, PrincipalTypeAgent, PrincipalTypeService)
}
