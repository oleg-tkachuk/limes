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

// Absence names an interface v is not, or is with it turned off, and
// nothing for one v is with it on.
func TestAbsence(t *testing.T) {
	readerType := reflect.TypeFor[reader]()
	off := func(iface reflect.Type) bool { return iface != readerType }
	cases := []struct {
		name    string
		v       any
		enabled Enabled
		want    string
	}{
		{"implemented, no gate", both{}, nil, ""},
		{"implemented and on", both{}, func(reflect.Type) bool { return true }, ""},
		{"implemented and off", both{}, off, fmt.Sprintf("%s (off by its configuration)", readerType)},
		{"not implemented", plain{}, nil, readerType.String()},
		{"nil", nil, nil, readerType.String()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Absence(tc.v, readerType, tc.enabled); got != tc.want {
				t.Fatalf("Absence = %q, want %q", got, tc.want)
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
	want := []string{reflect.TypeFor[reader]().String(), reflect.TypeFor[writer]().String()}
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
