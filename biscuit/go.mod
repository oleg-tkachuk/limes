// The Biscuit form of a limes capability token, which its holder attenuates
// offline (docs/adr/0008-biscuit-module.md). A module of its own so that
// limes itself never resolves biscuit-go or protobuf.
module github.com/oleg-tkachuk/limes/biscuit

go 1.27.0

toolchain go1.27.3

// The replace builds this module against the limes of the same commit, which
// is what CI tests; a consumer of this module, whose build ignores the
// replace, gets the version required here.
replace github.com/oleg-tkachuk/limes => ../

require (
	github.com/eclipse-biscuit/biscuit-go/v2 v2.2.2
	github.com/oleg-tkachuk/limes v0.144.0
	google.golang.org/protobuf v1.36.12
)

require golang.org/x/text v0.43.0 // indirect
