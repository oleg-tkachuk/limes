// The OAuth token exchange that turns an identity provider's access token
// into a limes capability (docs/adr/0003-oauth-token-exchange.md). A module
// of its own so that limes itself never resolves a JOSE library.
module github.com/oleg-tkachuk/limes/exchange

go 1.27.0

toolchain go1.27.4

// The replace builds this module against the limes of the same commit, which
// is what CI tests; a consumer of this module, whose build ignores the
// replace, gets the version required here.
replace github.com/oleg-tkachuk/limes => ../

require (
	github.com/lestrrat-go/httprc/v3 v3.0.6
	github.com/lestrrat-go/jwx/v4 v4.6.0
	github.com/oleg-tkachuk/limes v0.185.0
)

require (
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/dsig v1.4.0 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/option/v2 v2.0.0 // indirect
	github.com/lestrrat-go/option/v3 v3.0.0-alpha1 // indirect
	golang.org/x/text v0.43.0 // indirect
)
