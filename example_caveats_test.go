package limes_test

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

func ExampleCaveats_CheckSource() {
	caveats := limes.Caveats{Ops: []limes.Op{limes.OpGet}, SourceIPCIDR: []string{"10.0.0.0/8"}}

	fmt.Println(caveats.CheckSource(netip.MustParseAddr("10.1.2.3")))
	fmt.Println(errors.Is(caveats.CheckSource(netip.MustParseAddr("192.0.2.1")), limes.ErrSourceIPNotAllowed))
	// An address the consumer could not observe fails closed.
	fmt.Println(errors.Is(caveats.CheckSource(netip.Addr{}), limes.ErrSourceIPNotAllowed))
	// Output:
	// <nil>
	// true
	// true
}

func ExampleCaveats_AllowsResource() {
	caveats := limes.Caveats{
		Ops:              []limes.Op{limes.OpGet},
		ResourcePrefixes: []string{"corpus/public/"},
		ResourceURIs:     []string{"reports/q3.pdf"},
	}
	fmt.Println(caveats.RestrictsResources())
	fmt.Println(caveats.AllowsResource("corpus/public/a.txt"), caveats.AllowsResource("reports/q3.pdf"))
	// A dot segment proves nothing about where it leads, so it matches nothing.
	fmt.Println(caveats.AllowsResource("corpus/public/../private/a.txt"), caveats.AllowsResource(""))

	// No resource caveat: any resource within the tenant.
	open := limes.Caveats{Ops: []limes.Op{limes.OpGet}}
	fmt.Println(open.RestrictsResources(), open.AllowsResource("anything"))
	// Output:
	// true
	// true true
	// false false
	// false true
}

func ExampleCaveats_Validate() {
	fmt.Println(limes.Caveats{Ops: []limes.Op{limes.OpGet, "tool:search"}}.Validate())

	err := limes.Caveats{Ops: []limes.Op{limes.OpGet}, SourceIPCIDR: []string{"not-a-cidr"}}.Validate()
	fmt.Println(errors.Is(err, limes.ErrInvalidCaveats), errors.Is(err, limes.ErrInvalidRequest))
	// Output:
	// <nil>
	// true true
}

func ExampleCaveats_Check_idempotencyAndTaint() {
	caveats := limes.Caveats{
		Ops:                    []limes.Op{limes.OpGet, limes.OpPut},
		IdempotencyKeyRequired: true,
	}
	// A write without an idempotency key is refused; a read needs none.
	err := caveats.Check(limes.CheckRequest{Op: limes.OpPut, Resource: "notes/1"})
	fmt.Println(errors.Is(err, limes.ErrIdempotencyKeyRequired))
	fmt.Println(caveats.Check(limes.CheckRequest{Op: limes.OpPut, Resource: "notes/1", HasIdempotencyKey: true}))

	// A read of a resource the consumer flagged is refused unless allowed.
	err = caveats.Check(limes.CheckRequest{Op: limes.OpGet, Resource: "inbox/7", ResourceTainted: true})
	fmt.Println(errors.Is(err, limes.ErrTaintedReadNotAllowed))
	// Output:
	// true
	// <nil>
	// true
}

func ExampleCheckRequest_Mutating() {
	write, _ := limes.CheckRequest{Op: limes.OpPut}.Mutating()
	read, _ := limes.CheckRequest{Op: limes.OpList}.Mutating()
	// A consumer-defined operation is a write unless the server declares it.
	custom, _ := limes.CheckRequest{Op: "index:search"}.Mutating()
	declared, _ := limes.CheckRequest{Op: "index:search", Effect: limes.EffectRead}.Mutating()
	fmt.Println(write, read, custom, declared)

	// A declaration may not change a built-in operation's effect.
	_, err := limes.CheckRequest{Op: limes.OpPut, Effect: limes.EffectRead}.Mutating()
	fmt.Println(errors.Is(err, limes.ErrEffectConflict))
	// Output:
	// true false true false
	// true
}

func ExampleOp_Validate() {
	fmt.Println(limes.OpGet.Validate(), limes.Op("mcp:github/create_issue").Validate())
	fmt.Println(limes.Op("search").Validate())
	// Output:
	// <nil> <nil>
	// limes: invalid caveats: unknown op "search": the built-in ops are get, put, list, delete, share, manage; a custom op is namespaced, e.g. "tool:search"
}

func ExampleOp_Builtin() {
	fmt.Println(limes.OpShare.Builtin(), limes.Op("tool:search").Builtin())
	// Output:
	// true false
}

func ExampleOp_ResolveEffect() {
	fmt.Println(limes.OpGet.ResolveEffect(limes.EffectUnspecified))
	fmt.Println(limes.Op("model:embed").ResolveEffect(limes.EffectUnspecified))
	fmt.Println(limes.Op("model:embed").ResolveEffect(limes.EffectRead))
	// Output:
	// read <nil>
	// write <nil>
	// read <nil>
}

func ExamplePrincipal_Validate() {
	agent := limes.Principal{
		Type: limes.PrincipalTypeAgent, TenantID: uuid.New(), Subject: "research-orchestrator",
		Agent: &limes.AgentPrincipal{AgentType: "researcher", Model: "example-model-1"},
	}
	fmt.Println(agent.Validate())

	// Agent details belong to an agent only.
	service := agent
	service.Type = limes.PrincipalTypeService
	fmt.Println(errors.Is(service.Validate(), limes.ErrInvalidRequest))
	fmt.Println(errors.Is(limes.PrincipalType("robot").Validate(), limes.ErrInvalidRequest))
	// Output:
	// <nil>
	// true
	// true
}

func ExampleCapability_Clone() {
	c := limes.Capability{Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}}}
	clone := c.Clone()
	clone.Caveats.Ops[0] = limes.OpDelete
	// The clone's slices are its own.
	fmt.Println(c.Caveats.Ops, clone.Caveats.Ops)
	// Output:
	// [get] [delete]
}

func ExampleNarrows() {
	start := time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
	tenant := uuid.New()
	parent := limes.Capability{
		Subject:   limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "orchestrator"},
		Audience:  []string{exampleAudience},
		IssuedAt:  start,
		ExpiresAt: start.Add(time.Hour),
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet, limes.OpShare},
			ResourcePrefixes: []string{"corpus/"},
			Requests:         limes.RequestCeiling{Max: 100},
		},
	}
	child := parent
	child.Caveats = limes.Caveats{
		Ops:              []limes.Op{limes.OpGet},
		ResourcePrefixes: []string{"corpus/public/"},
		Requests:         limes.RequestCeiling{Max: 10},
	}
	// A dry run of a delegation, with no issuer in the path.
	fmt.Println(limes.Narrows(parent, child))

	child.Caveats.Ops = []limes.Op{limes.OpDelete}
	fmt.Println(errors.Is(limes.Narrows(parent, child), limes.ErrDelegationTooWide))
	// Output:
	// <nil>
	// true
}
