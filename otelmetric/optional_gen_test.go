package otelmetric

import (
	"bytes"
	"os"
	"testing"

	"github.com/oleg-tkachuk/limes/otelmetric/internal/optionalgen"
)

// The checked-in withOptional is the one optionalgen writes: an interface
// added to its list and not generated fails here.
func TestOptionalIsGenerated(t *testing.T) {
	want, err := optionalgen.Source()
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile(optionalgen.File)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(have, want) {
		t.Errorf("%s is not what internal/optionalgen writes; run go generate in otelmetric", optionalgen.File)
	}
}
