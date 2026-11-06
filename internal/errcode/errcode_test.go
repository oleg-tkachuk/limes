package errcode

import (
	"net/http"
	"slices"
	"testing"
)

// answered is an error carrying its whole answer.
type answered string

func (a answered) Error() string              { return string(a) }
func (a answered) LimesErrorCode() string     { return string(a) }
func (a answered) LimesErrorStatus() int      { return http.StatusBadRequest }
func (a answered) LimesErrorTitle() string    { return string(a) }
func (a answered) LimesErrorCategory() string { return "invalid" }

// testComponent registers in this test alone.
const testComponent = "test"

// Registered lists every registration in the order made, under its
// component, and a copy: a caller changing it changes no registration.
func TestRegisterKeepsOrderAndComponent(t *testing.T) {
	before := len(Registered())
	Register(testComponent, answered("first"), answered("second"))
	Register(ComponentExchange, answered("third"))
	got := Registered()[before:]
	want := []Registration{
		{Component: testComponent, Err: answered("first")},
		{Component: testComponent, Err: answered("second")},
		{Component: ComponentExchange, Err: answered("third")},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Registered() = %v, want %v", got, want)
	}
	got[0].Component = "changed"
	if Registered()[before].Component != testComponent {
		t.Error("changing what Registered returned changed the registration")
	}
}
