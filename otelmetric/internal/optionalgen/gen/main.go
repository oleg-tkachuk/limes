// Command gen writes otelmetric's optional_gen.go from optionalgen.Optional;
// run by go generate in otelmetric.
package main

import (
	"log"
	"os"

	"github.com/oleg-tkachuk/limes/otelmetric/internal/optionalgen"
)

// fileMode is the generated file's permissions: a source file's.
const fileMode = 0o644

func main() {
	src, err := optionalgen.Source()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(optionalgen.File, src, fileMode); err != nil {
		log.Fatal(err)
	}
}
