package fields

import (
	"slices"
	"testing"
	"time"
	"uuid"
)

type inner struct {
	N    int
	Flag bool
}

type outer struct {
	ID     uuid.UUID
	At     time.Time
	Name   string
	Raw    []byte
	Ptr    *inner
	List   []inner
	Map    map[string]int
	Nested inner
	_      [0]func()
}

func TestUnset(t *testing.T) {
	full := outer{
		ID: uuid.New(), At: time.Unix(1, 0), Name: "n", Raw: []byte{0},
		Ptr: &inner{N: 1, Flag: true}, List: []inner{{N: 1, Flag: true}},
		Map: map[string]int{"a": 0}, Nested: inner{N: 1, Flag: true},
	}
	tests := []struct {
		name string
		v    any
		want []string
	}{
		{"every field set", full, nil},
		{"through a pointer", &full, nil},
		{"all zero", outer{}, []string{"ID", "At", "Name", "Raw", "Ptr", "List", "Map", "Nested.N", "Nested.Flag"}},
		{"inside a pointer and a slice element", func() outer {
			o := full
			o.Ptr = &inner{N: 1}
			o.List = []inner{{N: 1, Flag: true}, {Flag: true}}
			return o
		}(), []string{"Ptr.Flag", "List[1].N"}},
		{"an empty but non-nil slice", func() outer { o := full; o.List = []inner{}; return o }(), []string{"List"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Unset(tc.v); !slices.Equal(got, tc.want) {
				t.Errorf("Unset = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNames(t *testing.T) {
	want := []string{"ID", "At", "Name", "Raw", "Ptr", "List", "Map", "Nested"}
	if got := Names[outer](); !slices.Equal(got, want) {
		t.Errorf("Names = %q, want %q", got, want)
	}
}
