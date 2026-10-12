package limes

import (
	"reflect"
	"testing"
)

// The exported types carry no wire format, and the wire structs carry every
// field of theirs: a field added to Caveats but not to wireCaveats would be
// dropped from every token without a word.
func TestWireStructsMirrorTheExportedTypes(t *testing.T) {
	pairs := []struct{ exported, wire reflect.Type }{
		{reflect.TypeFor[Principal](), reflect.TypeFor[wirePrincipal]()},
		{reflect.TypeFor[AgentPrincipal](), reflect.TypeFor[wireAgent]()},
		{reflect.TypeFor[Caveats](), reflect.TypeFor[wireCaveats]()},
	}
	for _, p := range pairs {
		if p.exported.NumField() != p.wire.NumField() {
			t.Errorf("%s has %d fields, %s %d", p.exported.Name(), p.exported.NumField(), p.wire.Name(), p.wire.NumField())
		}
		for i := range p.exported.NumField() {
			f := p.exported.Field(i)
			if tag, ok := f.Tag.Lookup("json"); ok {
				t.Errorf("%s.%s carries json tag %q; the wire format lives in %s", p.exported.Name(), f.Name, tag, p.wire.Name())
			}
			if _, ok := p.wire.FieldByName(f.Name); !ok {
				t.Errorf("%s.%s has no counterpart in %s", p.exported.Name(), f.Name, p.wire.Name())
			}
		}
	}
	capability := reflect.TypeFor[Capability]()
	for i := range capability.NumField() {
		if tag, ok := capability.Field(i).Tag.Lookup("json"); ok {
			t.Errorf("Capability.%s carries json tag %q", capability.Field(i).Name, tag)
		}
	}
}

// Every field crosses the wire and back.
func TestWireStructsRoundTrip(t *testing.T) {
	p := Principal{Type: PrincipalAgent, Subject: "agent:a", Agent: &AgentPrincipal{
		AgentType: "worker", AgentVersion: "1", Model: "m", MCPClient: "host@1",
	}}
	if got := toWirePrincipal(p).principal(); !reflect.DeepEqual(got, p) {
		t.Errorf("principal round trip = %+v, want %+v", got, p)
	}
	c := Caveats{
		Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/"}, ResourceURIs: []string{"a/b"},
		MaxRequests: 1, MaxBudgetAmount: NanosPerUnit, UnitCode: DefaultUnitCode,
		AllowTaintedRead: true, IdempotencyKeyRequired: true, SourceIPCIDR: []string{"10.0.0.0/8"},
	}
	if got := toWireCaveats(c).caveats(); !reflect.DeepEqual(got, c) {
		t.Errorf("caveats round trip = %+v, want %+v", got, c)
	}
}
