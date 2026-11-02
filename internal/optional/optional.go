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

// Absence names iface when v is not one, or is and its configuration turned
// it off: "" when v is iface and enabled says so.
func Absence(v any, iface reflect.Type, enabled Enabled) string {
	if v == nil || !reflect.TypeOf(v).Implements(iface) {
		return iface.String()
	}
	if enabled != nil && !enabled(iface) {
		return fmt.Sprintf("%s (off by its configuration)", iface)
	}
	return ""
}

// Assign sets each target, a pointer to an interface, to v when v is that
// interface, and returns why each other one is not, in the targets' order.
// A target that is no pointer to an interface is invalid, the caller's
// sentinel for a bad request, before any is set.
func Assign(v any, enabled Enabled, invalid error, targets ...any) ([]string, error) {
	pointers := make([]reflect.Value, len(targets))
	for i, t := range targets {
		p := reflect.ValueOf(t)
		if p.Kind() != reflect.Pointer || p.IsNil() || p.Elem().Kind() != reflect.Interface {
			return nil, fmt.Errorf("%w: %T is no pointer to an interface", invalid, t)
		}
		pointers[i] = p
	}
	var missing []string
	for _, p := range pointers {
		iface := p.Elem().Type()
		if why := Absence(v, iface, enabled); why != "" {
			missing = append(missing, why)
			continue
		}
		p.Elem().Set(reflect.ValueOf(v))
	}
	return missing, nil
}
