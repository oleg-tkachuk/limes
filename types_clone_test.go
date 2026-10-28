package limes

import (
	"net/netip"
	"testing"
)

// Clone shares nothing with the capability it copies.
func TestCapabilityCloneSharesNothing(t *testing.T) {
	c := Capability{
		Subject:  Principal{Type: PrincipalTypeAgent, Agent: &AgentPrincipal{Model: "m"}},
		Audience: []string{"a"},
		Caveats:  Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}, ResourcePrefixes: []string{"p/"}, ResourceURIs: []string{"p/u"}, SourceIPCIDR: []string{netip.MustParsePrefix("10.0.0.0/8").String()}},
	}
	d := c.Clone()
	d.Subject.Agent.Model = "x"
	d.Audience[0] = "x"
	d.Caveats.Ops[0] = "x"
	d.Caveats.ResourcePrefixes[0] = "x"
	d.Caveats.ResourceURIs[0] = "x"
	d.Caveats.SourceIPCIDR[0] = "x"
	if c.Subject.Agent.Model != "m" || c.Audience[0] != "a" || c.Caveats.Ops[0] != OpGet ||
		c.Caveats.ResourcePrefixes[0] != "p/" || c.Caveats.ResourceURIs[0] != "p/u" ||
		c.Caveats.SourceIPCIDR[0] != "10.0.0.0/8" {
		t.Errorf("changing the clone changed the original: %+v", c)
	}
	p := Principal{Agent: &AgentPrincipal{Model: "m"}}
	q := p.Clone()
	q.Agent.Model = "x"
	if p.Agent.Model != "m" {
		t.Error("changing a cloned principal changed the original")
	}
}
