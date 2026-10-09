package limes

import (
	"errors"
	"testing"
)

// Consumer-defined operations used by the tests below.
const (
	opToolRetrieve Op = "tool:retrieve"
	opToolSend     Op = "tool:send_mail"
)

func TestOpResolveEffect(t *testing.T) {
	cases := []struct {
		name     string
		op       Op
		declared Effect
		want     Effect
		wantErr  error
	}{
		{"builtin read, undeclared", OpGet, EffectUnspecified, EffectRead, nil},
		{"builtin write, undeclared", OpPut, EffectUnspecified, EffectWrite, nil},
		{"builtin read, repeated", OpList, EffectRead, EffectRead, nil},
		{"builtin write, repeated", OpDelete, EffectWrite, EffectWrite, nil},
		{"builtin write declared a read", OpPut, EffectRead, 0, ErrEffectConflict},
		{"builtin read declared a write", OpGet, EffectWrite, 0, ErrEffectConflict},
		{"custom, undeclared", opToolRetrieve, EffectUnspecified, EffectWrite, nil},
		{"custom, declared read", opToolRetrieve, EffectRead, EffectRead, nil},
		{"custom, declared write", opToolSend, EffectWrite, EffectWrite, nil},
		{"unknown effect", opToolRetrieve, EffectWrite + 1, 0, ErrEffectConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.op.ResolveEffect(tc.declared)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("ResolveEffect = %v, want %v in the ErrInvalidRequest family", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ResolveEffect = %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

// The undeclared default and Op.Mutating are one rule.
func TestOpMutatingMatchesDefaultEffect(t *testing.T) {
	for op := range builtinOps {
		want := op.defaultEffect() == EffectWrite
		if op.Mutating() != want {
			t.Errorf("%q: Mutating = %v, default effect %s", op, op.Mutating(), op.defaultEffect())
		}
	}
	if !opToolRetrieve.Mutating() {
		t.Errorf("an undeclared custom op must be mutating")
	}
}

func TestCheckHonoursDeclaredEffect(t *testing.T) {
	c := Caveats{
		Ops:                    []Op{OpPut, opToolRetrieve, opToolSend},
		IdempotencyKeyRequired: true,
	}
	const resource = "corpus/p/doc"
	cases := []struct {
		name string
		req  CheckRequest
		want error
	}{
		{"declared read needs no key", CheckRequest{Op: opToolRetrieve, Effect: EffectRead, Resource: resource}, nil},
		{"undeclared custom op needs a key", CheckRequest{Op: opToolRetrieve, Resource: resource}, ErrIdempotencyKeyRequired},
		{"declared write needs a key", CheckRequest{Op: opToolSend, Effect: EffectWrite, Resource: resource}, ErrIdempotencyKeyRequired},
		{"declared read of a tainted resource", CheckRequest{Op: opToolRetrieve, Effect: EffectRead, Resource: resource, ResourceTainted: true}, ErrTaintedReadNotAllowed},
		{"undeclared custom op on a tainted resource", CheckRequest{Op: opToolRetrieve, Resource: resource, ResourceTainted: true, HasIdempotencyKey: true}, nil},
		{"builtin write declared a read", CheckRequest{Op: OpPut, Effect: EffectRead, Resource: resource}, ErrEffectConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Check(tc.req)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Check = %v, want %v", err, tc.want)
			}
		})
	}
}

// A conflicting declaration is a programming error, not a caveat violation:
// it must not be mapped to "permission denied" alongside the real refusals.
func TestEffectConflictIsNotACaveatViolation(t *testing.T) {
	err := Caveats{Ops: []Op{OpPut}}.Check(CheckRequest{Op: OpPut, Effect: EffectRead, Resource: "r"})
	if !errors.Is(err, ErrEffectConflict) || errors.Is(err, ErrCaveatViolation) {
		t.Fatalf("Check = %v, want ErrEffectConflict outside ErrCaveatViolation", err)
	}
}

func TestEffectString(t *testing.T) {
	for e, want := range map[Effect]string{
		EffectUnspecified: "unspecified", EffectRead: "read", EffectWrite: "write", EffectWrite + 1: "Effect(3)",
	} {
		if got := e.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", uint8(e), got, want)
		}
	}
}
