package limes

import (
	"errors"
	"net/netip"
	"testing"
)

// ─── request-time checks ───────────────────────────────────────────────────

func TestCaveatsCheck(t *testing.T) {
	c := Caveats{
		Ops:                    []Op{OpGet, OpPut, OpList},
		ResourcePrefixes:       []string{"object://t/c/"},
		IdempotencyKeyRequired: true,
	}
	cases := []struct {
		name string
		req  CheckRequest
		want error
	}{
		{"allowed read", CheckRequest{Op: OpGet, Resource: "object://t/c/k"}, nil},
		{"op not granted", CheckRequest{Op: OpDelete, Resource: "object://t/c/k"}, ErrOpNotAllowed},
		{"outside prefix", CheckRequest{Op: OpGet, Resource: "object://t/other/k"}, ErrResourceNotAllowed},
		{"unscoped op on scoped capability", CheckRequest{Op: OpList}, ErrResourceNotAllowed},
		{"scoped listing", CheckRequest{Op: OpList, Resource: "object://t/c/"}, nil},
		{"mutation without key", CheckRequest{Op: OpPut, Resource: "object://t/c/k"}, ErrIdempotencyKeyRequired},
		{"mutation with key", CheckRequest{Op: OpPut, Resource: "object://t/c/k", HasIdempotencyKey: true}, nil},
		{"tainted read", CheckRequest{Op: OpGet, Resource: "object://t/c/k", ResourceTainted: true}, ErrTaintedReadNotAllowed},
	}
	for _, tc := range cases {
		err := c.Check(tc.req)
		switch {
		case tc.want == nil && err != nil:
			t.Errorf("%s: Check = %v, want nil", tc.name, err)
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s: Check = %v, want %v", tc.name, err, tc.want)
		case tc.want != nil && !errors.Is(err, ErrCaveatViolation):
			t.Errorf("%s: %v does not match the ErrCaveatViolation family", tc.name, err)
		}
	}

	c.AllowTaintedRead = true
	if err := c.Check(CheckRequest{Op: OpGet, Resource: "object://t/c/k", ResourceTainted: true}); err != nil {
		t.Errorf("tainted read with AllowTaintedRead: %v", err)
	}
}

func TestCaveatsCheckSource(t *testing.T) {
	c := Caveats{SourceIPCIDR: []string{"10.0.0.0/8"}}
	if err := c.CheckSource(netip.MustParseAddr("10.1.2.3")); err != nil {
		t.Errorf("inside: %v", err)
	}
	if err := c.CheckSource(netip.MustParseAddr("::ffff:10.1.2.3")); err != nil {
		t.Errorf("v4-mapped inside: %v", err)
	}
	if err := c.CheckSource(netip.MustParseAddr("11.0.0.1")); !errors.Is(err, ErrSourceIPNotAllowed) {
		t.Errorf("outside: %v", err)
	}
	if err := c.CheckSource(netip.Addr{}); !errors.Is(err, ErrSourceIPNotAllowed) {
		t.Errorf("unknown address must fail closed, got %v", err)
	}
	if err := (Caveats{}).CheckSource(netip.Addr{}); err != nil {
		t.Errorf("unconstrained: %v", err)
	}
}
