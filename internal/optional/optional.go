// Package optional hands back a store as the optional interfaces a caller
// asks for, naming every one it lacks, for limes.As and meter.As.
package optional

import (
	"fmt"
	"reflect"
)

// Enabled reports whether a store that implements iface also has it enabled
// by its configuration; nil, every interface implemented is.
type Enabled func(iface reflect.Type) bool

// Absent is an interface a store is not, or is with its configuration
// turning it off.
type Absent struct {
	Iface reflect.Type
	// Disabled: the store implements Iface, and its configuration turned it
	// off. Otherwise it does not implement it.
	Disabled bool
}

// String names the interface, and says when it is off.
func (a Absent) String() string {
	if a.Disabled {
		return fmt.Sprintf("%s (off by its configuration)", a.Iface)
	}
	return a.Iface.String()
}

// Check reports whether v is iface with enabled saying so, and when it is
// not, why.
func Check(v any, iface reflect.Type, enabled Enabled) (Absent, bool) {
	if v == nil || !reflect.TypeOf(v).Implements(iface) {
		return Absent{Iface: iface}, false
	}
	if enabled != nil && !enabled(iface) {
		return Absent{Iface: iface, Disabled: true}, false
	}
	return Absent{}, true
}

// Assign sets each target, a pointer to an interface, to v when v is that
// interface, and returns why each other one is not, in the targets' order,
// leaving it unset. A target that is no pointer to an interface is invalid,
// the caller's sentinel for a bad request, before any is set.
func Assign(v any, enabled Enabled, invalid error, targets ...any) ([]Absent, error) {
	pointers := make([]reflect.Value, len(targets))
	for i, t := range targets {
		p := reflect.ValueOf(t)
		if p.Kind() != reflect.Pointer || p.IsNil() || p.Elem().Kind() != reflect.Interface {
			return nil, fmt.Errorf("%w: %T is no pointer to an interface", invalid, t)
		}
		pointers[i] = p
	}
	var absent []Absent
	for _, p := range pointers {
		a, ok := Check(v, p.Elem().Type(), enabled)
		if !ok {
			absent = append(absent, a)
			continue
		}
		p.Elem().Set(reflect.ValueOf(v))
	}
	return absent, nil
}
