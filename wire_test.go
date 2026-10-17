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
	}
	for _, p := range pairs {
		if p.exported.NumField() != p.wire.NumField() {
			t.Errorf("%s has %d fields, %s %d", p.exported.Name(), p.exported.NumField(), p.wire.Name(), p.wire.NumField())
		}
		for i := range p.exported.NumField() {
			f := p.exported.Field(i)
			if _, ok := p.wire.FieldByName(f.Name); !ok {
				t.Errorf("%s.%s has no counterpart in %s", p.exported.Name(), f.Name, p.wire.Name())
			}
		}
	}
	// A ceiling is its members on the wire: its bound, whether it is
	// lifted and, for a budget, its unit. Every other field of Caveats has
	// one of the same name.
	ceilings := map[string][]string{
		"Requests": {"MaxRequests", "UnlimitedRequests"},
		"Budget":   {"MaxBudget", "UnlimitedBudget", "UnitCode"},
	}
	caveats, wire := reflect.TypeFor[Caveats](), reflect.TypeFor[wireCaveats]()
	want := 0
	for i := range caveats.NumField() {
		f := caveats.Field(i)
		names, isCeiling := ceilings[f.Name]
		if !isCeiling {
			names = []string{f.Name}
		}
		for _, n := range names {
			want++
			if _, ok := wire.FieldByName(n); !ok {
				t.Errorf("Caveats.%s has no %s in wireCaveats", f.Name, n)
			}
		}
	}
	if wire.NumField() != want {
		t.Errorf("wireCaveats has %d fields, want %d", wire.NumField(), want)
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[Principal](), reflect.TypeFor[AgentPrincipal](), caveats, reflect.TypeFor[Capability]()} {
		for i := range typ.NumField() {
			if tag, ok := typ.Field(i).Tag.Lookup("json"); ok {
				t.Errorf("%s.%s carries json tag %q; the wire format lives in wire.go", typ.Name(), typ.Field(i).Name, tag)
			}
		}
	}
}

// Every field crosses the wire and back.
func TestWireStructsRoundTrip(t *testing.T) {
	p := Principal{Type: PrincipalTypeAgent, Subject: "agent:a", Agent: &AgentPrincipal{
		AgentType: "worker", AgentVersion: "1", Model: "m", MCPClient: "host@1",
	}}
	if got := toWirePrincipal(p).principal(); !reflect.DeepEqual(got, p) {
		t.Errorf("principal round trip = %+v, want %+v", got, p)
	}
	c := Caveats{
		Ops: []Op{OpGet}, ResourcePrefixes: []string{"a/"}, ResourceURIs: []string{"a/b"},
		Requests: RequestCeiling{Max: 1}, Budget: BudgetCeiling{Max: NanosPerUnit, UnitCode: "USD"},
		AllowTaintedRead: true, IdempotencyKeyRequired: true, SourceIPCIDR: []string{"10.0.0.0/8"},
	}
	if got := toWireCaveats(c).caveats(); !reflect.DeepEqual(got, c) {
		t.Errorf("caveats round trip = %+v, want %+v", got, c)
	}
}
