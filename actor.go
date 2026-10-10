package limes

import "fmt"

// Delegation in the terms of OAuth 2.0 Token Exchange, RFC 8693 §4.1.
//
// A delegated capability's token says on whose authority it acts and who is
// acting: its `sub` is the subject of the root capability the chain started
// from, and its `act` claim names the holder of this capability, nesting the
// holders it was delegated through — the outermost the current actor, as
// §4.1 orders them. A root capability has no `act`, and its `sub` is its own
// subject.

// MaxDelegationDepth bounds a delegation chain: the nesting of an act claim
// a token may carry, and so how many times a capability may be delegated
// from a root. It matches the lineage a Store walks.
const MaxDelegationDepth = 64

// Actor is an RFC 8693 §4.1 act claim: the party acting, and in Actor the
// party it was delegated from.
type Actor struct {
	Subject string `json:"sub"`
	Actor   *Actor `json:"act,omitzero"`
}

// depth is the number of actors in the chain a starts.
func (a *Actor) depth() int {
	n := 0
	for ; a != nil; a = a.Actor {
		n++
	}
	return n
}

// clone is a deep copy of the chain a starts, so no two capabilities share
// one.
func (a *Actor) clone() *Actor {
	if a == nil {
		return nil
	}
	return &Actor{Subject: a.Subject, Actor: a.Actor.clone()}
}

// Authority is the subject on whose authority c acts: OnBehalfOf for a
// delegated capability, its own subject for a root one.
func (c Capability) Authority() string {
	if c.Actor != nil {
		return c.OnBehalfOf
	}
	return c.Subject.Subject
}

// delegatedActor is the act chain of a capability delegated from parent to
// subject: subject acting, after parent's actors.
func delegatedActor(parent Capability, subject string) (*Actor, error) {
	if d := parent.Actor.depth() + 1; d > MaxDelegationDepth {
		return nil, invalidRequest("delegation %d deep, past MaxDelegationDepth %d", d, MaxDelegationDepth)
	}
	return &Actor{Subject: subject, Actor: parent.Actor.clone()}, nil
}

// checkActor holds a token's act claim to RFC 8693 and to the principal it
// names: the current actor is the capability's own subject, and the chain is
// no deeper than MaxDelegationDepth.
func checkActor(act *Actor, principal string) error {
	if act == nil {
		return nil
	}
	if act.Subject != principal {
		return fmt.Errorf("limes: act.sub %q is not the principal %q", act.Subject, principal)
	}
	if d := act.depth(); d > MaxDelegationDepth {
		return fmt.Errorf("limes: act claim %d deep, past %d", d, MaxDelegationDepth)
	}
	return nil
}
