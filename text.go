package limes

import "github.com/oleg-tkachuk/limes/internal/storable"

// checkText refuses, as ErrInvalidRequest, texts a store cannot keep as
// they are: invalid UTF-8, or a NUL (internal/storable). Every request that
// hands a store text checks it here, so every store refuses the same input
// as the caller's, never failing on it as the server's. what names the
// request; the text itself is not repeated, being what could not be kept.
func checkText(what string, texts ...string) error {
	if !storable.Text(texts...) {
		return invalidRequest("%s: text that is not valid UTF-8 or holds a NUL", what)
	}
	return nil
}

// texts are the text p carries: its subject and its agent's details.
func (p Principal) texts() []string {
	out := []string{p.Subject}
	if a := p.Agent; a != nil {
		out = append(out, a.AgentType, a.AgentVersion, a.Model, a.MCPClient)
	}
	return out
}

// texts are the text c carries, which a store keeps whole.
func (c Capability) texts() []string {
	out := []string{c.Issuer, c.ConfirmationJKT, c.ClientID}
	out = append(out, c.Subject.texts()...)
	if c.OnBehalfOf != nil {
		out = append(out, c.OnBehalfOf.texts()...)
	}
	out = append(out, c.Audience...)
	out = append(out, c.PriorActors...)
	return append(out, c.Caveats.texts()...)
}

// texts are the text c carries.
func (c Caveats) texts() []string {
	out := []string{c.Budget.UnitCode}
	for _, op := range c.Ops {
		out = append(out, string(op))
	}
	out = append(out, c.ResourcePrefixes...)
	out = append(out, c.ResourceURIs...)
	out = append(out, c.ResourceNamePrefixes...)
	return append(out, c.SourceIPCIDR...)
}
