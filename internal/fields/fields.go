// Package fields lets the contract suites, storetest and metertest, prove
// that what they write and read back covers every field of the module's
// types. A store keeps its own representation of a record — columns, or a
// mirror struct it serialises — and a field that representation lacks reads
// back as its zero value. A round trip only catches that when the value sent
// was not zero, so the suites check their fixtures, and the records a store
// returns, for fields left unset.
package fields

import (
	"reflect"
	"strconv"
	"time"
)

// Unset names, as dotted paths, every field of v that holds its zero value:
// a zero scalar, a nil pointer, an empty slice or map. It walks into structs,
// through non-nil pointers and into each slice element, and skips blank
// fields. A time.Time is one value, unset when IsZero; so is a []byte.
func Unset(v any) []string {
	var out []string
	walk(reflect.ValueOf(v), "", &out)
	return out
}

var timeType = reflect.TypeFor[time.Time]()

func walk(v reflect.Value, path string, out *[]string) {
	switch {
	case v.Type() == timeType:
		if v.Interface().(time.Time).IsZero() {
			*out = append(*out, path)
		}
		return
	case v.Kind() == reflect.Pointer:
		if v.IsNil() {
			*out = append(*out, path)
			return
		}
		walk(v.Elem(), path, out)
		return
	case v.Kind() == reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if f.Name == "_" {
				continue
			}
			walk(v.Field(i), join(path, f.Name), out)
		}
		return
	case v.Kind() == reflect.Slice || v.Kind() == reflect.Map:
		if v.Len() == 0 {
			*out = append(*out, path)
			return
		}
		// Bytes are one value: a zero byte inside them is data.
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8 {
			for i := range v.Len() {
				walk(v.Index(i), path+"["+strconv.Itoa(i)+"]", out)
			}
		}
		return
	}
	if v.IsZero() {
		*out = append(*out, path)
	}
}

// Names lists the exported fields of the struct type T, in declaration
// order: what a suite checks a type against, so a field the module adds
// fails the suite until a check covers it.
func Names[T any]() []string {
	t := reflect.TypeFor[T]()
	var out []string
	for i := range t.NumField() {
		if f := t.Field(i); f.IsExported() {
			out = append(out, f.Name)
		}
	}
	return out
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
