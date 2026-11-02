package optional

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

type (
	reader interface{ Read() }
	writer interface{ Write() }
	both   struct{}
	plain  struct{}
)

func (both) Read()  {}
func (both) Write() {}

var errBad = errors.New("bad request")

// Check tells an interface v is not from one v is with it turned off, and
// passes one v is with it on.
func TestCheck(t *testing.T) {
	readerType := reflect.TypeFor[reader]()
	off := func(iface reflect.Type) bool { return iface != readerType }
	cases := []struct {
		name    string
		v       any
		enabled Enabled
		ok      bool
		want    Absent
		named   string
	}{
		{"implemented, no gate", both{}, nil, true, Absent{}, ""},
		{"implemented and on", both{}, func(reflect.Type) bool { return true }, true, Absent{}, ""},
		{"implemented and off", both{}, off, false, Absent{Iface: readerType, Disabled: true}, fmt.Sprintf("%s (off by its configuration)", readerType)},
		{"not implemented", plain{}, nil, false, Absent{Iface: readerType}, readerType.String()},
		{"nil", nil, nil, false, Absent{Iface: readerType}, readerType.String()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Check(tc.v, readerType, tc.enabled)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("Check = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
			if !ok && got.String() != tc.named {
				t.Errorf("String = %q, want %q", got, tc.named)
			}
		})
	}
}

// Assign sets every target v is, names every other one in order, and sets
// none when a target is no pointer to an interface.
func TestAssign(t *testing.T) {
	var r reader
	var w writer
	missing, err := Assign(both{}, nil, errBad, &r, &w)
	if err != nil || len(missing) != 0 || r == nil || w == nil {
		t.Fatalf("Assign(both) = %v, %v; reader %v, writer %v", missing, err, r, w)
	}
	r, w = nil, nil
	missing, err = Assign(plain{}, nil, errBad, &r, &w)
	want := []Absent{{Iface: reflect.TypeFor[reader]()}, {Iface: reflect.TypeFor[writer]()}}
	if err != nil || !slices.Equal(missing, want) || r != nil || w != nil {
		t.Fatalf("Assign(plain) = %v, %v; want %v and nothing set", missing, err, want)
	}
	var nilReader *reader
	for _, bad := range []any{r, both{}, &both{}, nilReader} {
		w = nil
		if _, err := Assign(both{}, nil, errBad, &w, bad); !errors.Is(err, errBad) || w != nil {
			t.Errorf("Assign with target %T = %v, writer %v; want the caller's sentinel and nothing set", bad, err, w)
		}
	}
}
