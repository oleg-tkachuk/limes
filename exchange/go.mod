// The OAuth token exchange that turns an identity provider's access token
// into a limes capability (docs/adr/0003-oauth-token-exchange.md). A module
// of its own so that limes itself never resolves a JOSE library.
module github.com/oleg-tkachuk/limes/exchange

go 1.27.0

toolchain go1.27.3

// The replace builds this module against the limes of the same commit, which
// is what CI tests; a consumer of this module, whose build ignores the
// replace, gets the version required here.
replace github.com/oleg-tkachuk/limes => ../

require (
	github.com/lestrrat-go/httprc/v3 v3.0.6
	github.com/lestrrat-go/jwx/v3 v3.4.0
	github.com/oleg-tkachuk/limes v0.128.0
)

require (
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/eclipse-biscuit/biscuit-go/v2 v2.2.2 // indirect
	github.com/goccy/go-json v0.11.2 // indirect
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/dsig v1.4.0 // indirect
	github.com/lestrrat-go/dsig-secp256k1 v1.0.0 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/option/v2 v2.0.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/valyala/fastjson v1.6.10 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
