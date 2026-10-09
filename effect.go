package limes

import "fmt"

// Effect is what an operation does to state. Check uses it twice: a mutating
// operation may need an idempotency key (Caveats.IdempotencyKeyRequired), and
// a read may be refused on a tainted resource (Caveats.AllowTaintedRead).
//
// The server that performs the operation declares the effect, from the same
// place it defines the operation (a method option, a tool registry), and
// never from anything the bearer sends.
type Effect uint8

const (
	// EffectUnspecified takes the operation's default: the fixed effect of a
	// built-in operation, and EffectWrite for a consumer-defined one, whose
	// effect this package cannot know — so an undeclared operation errs
	// towards the caveats that tighten mutations.
	EffectUnspecified Effect = iota
	// EffectRead leaves state unchanged.
	EffectRead
	// EffectWrite may change state.
	EffectWrite
)

// ErrEffectConflict is a declared effect that contradicts a built-in
// operation's own ("put" declared a read), or one this package does not
// define. A declaration may name a built-in operation's effect, never change
// it: a write declared a read would shed IdempotencyKeyRequired. It is a
// programming error in the consumer, so it matches ErrInvalidRequest.
var ErrEffectConflict error = &requestError{"limes: declared effect conflicts with the operation"}

// String names the effect for logs and errors.
func (e Effect) String() string {
	switch e {
	case EffectUnspecified:
		return "unspecified"
	case EffectRead:
		return "read"
	case EffectWrite:
		return "write"
	default:
		return fmt.Sprintf("Effect(%d)", uint8(e))
	}
}

// defaultEffect is op's effect with nothing declared: the fixed effect of a
// built-in operation, EffectWrite for any other.
func (op Op) defaultEffect() Effect {
	if readOnlyOps[op] {
		return EffectRead
	}
	return EffectWrite
}

// ResolveEffect returns the effect Check applies to op, given the effect the
// consumer declared for it. A built-in operation keeps its own effect, and a
// declaration may only repeat it; a consumer-defined operation takes the
// declaration, or EffectWrite when there is none.
func (op Op) ResolveEffect(declared Effect) (Effect, error) {
	if declared > EffectWrite {
		return 0, fmt.Errorf("%w: unknown effect %s for %q", ErrEffectConflict, declared, op)
	}
	if op.Builtin() {
		own := op.defaultEffect()
		if declared != EffectUnspecified && declared != own {
			return 0, fmt.Errorf("%w: %q is a built-in %s, declared %s", ErrEffectConflict, op, own, declared)
		}
		return own, nil
	}
	if declared == EffectUnspecified {
		return op.defaultEffect(), nil
	}
	return declared, nil
}
