// Package sample is a fixture of apidoc's tests.
package sample

// Limit bounds [Thing.Do]; it counts as a [time.Duration] does not.
const Limit = 3

// Thing does things.
type Thing struct {
	// Name names it.
	Name   string
	hidden int
}

// NewThing makes a [Thing].
func NewThing() *Thing { return &Thing{hidden: Limit} }

// Do does it, up to [Limit].
func (t *Thing) Do() error { return nil }
