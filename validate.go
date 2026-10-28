package limes

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
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
	if err := validateOps(c.Ops); err != nil {
		return err
	}
	if err := c.validateResources(); err != nil {
		return err
	}
	if err := c.validateLimits(); err != nil {
		return err
	}
	return validateSourceIPCIDRs(c.SourceIPCIDR)
}

// validateOps refuses no op at all, or one that is not well formed.
func validateOps(ops []Op) error {
	if len(ops) == 0 {
		return fmt.Errorf("%w: at least one op required", ErrInvalidCaveats)
	}
	for _, op := range ops {
		if err := op.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// validateResources refuses a resource prefix, URI or name prefix that is
// not well formed, or a name prefix that ends inside a character.
func (c Caveats) validateResources() error {
	if err := validResources("resource prefix", c.ResourcePrefixes); err != nil {
		return err
	}
	if err := validResources("resource URI", c.ResourceURIs); err != nil {
		return err
	}
	if err := validResources("resource name prefix", c.ResourceNamePrefixes); err != nil {
		return err
	}
	for _, n := range c.ResourceNamePrefixes {
		if err := wholeCharacters(n); err != nil {
			return err
		}
	}
	return nil
}

// validResources is validResource of each of values, a kind of resource.
func validResources(kind string, values []string) error {
	for _, v := range values {
		if err := validResource(kind, v); err != nil {
			return err
		}
	}
	return nil
}

// validateLimits refuses a request, budget or copy ceiling out of range.
func (c Caveats) validateLimits() error {
	if err := c.Requests.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCaveats, err)
	}
	if err := c.Budget.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCaveats, err)
	}
	if err := c.Copies.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCaveats, err)
	}
	return nil
}

// validateSourceIPCIDRs refuses a source prefix that is not a CIDR, or one
// no client can match.
func validateSourceIPCIDRs(cidrs []string) error {
	for _, cidr := range cidrs {
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

// wholeCharacters refuses a name prefix that ends inside a character: a
// percent-escape cut short ("%" or "%2") or a UTF-8 sequence cut short. A
// name prefix matches as a plain string, so one ending so would match only
// names that complete the character, but read decoded it names no
// character at all: the issuer cannot have meant it.
func wholeCharacters(prefix string) error {
	if !utf8.ValidString(prefix) {
		return fmt.Errorf("%w: resource name prefix %q is not whole UTF-8", ErrInvalidCaveats, prefix)
	}
	if i := strings.LastIndexByte(prefix, '%'); i >= 0 && len(prefix)-i < percentEscapeLen {
		return fmt.Errorf("%w: resource name prefix %q ends inside a percent-escape", ErrInvalidCaveats, prefix)
	}
	return nil
}

// percentEscapeLen is the length of a percent-escape: "%" and two hex digits
// (RFC 3986 §2.1).
const percentEscapeLen = 3

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
