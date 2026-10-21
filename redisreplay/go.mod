// A dpop.ReplayCache over Redis. A module of its own so that limes itself
// never resolves a Redis client.
module github.com/oleg-tkachuk/limes/redisreplay

go 1.26.0

toolchain go1.27.3

// The replace builds this module against the limes of the same commit, which
// is what CI tests; a consumer of this module, whose build ignores the
// replace, gets the version required here.
replace github.com/oleg-tkachuk/limes => ../

require (
	github.com/alicebob/miniredis/v2 v2.40.0
	github.com/google/uuid v1.6.0
	github.com/oleg-tkachuk/limes v0.111.0
	github.com/redis/go-redis/v9 v9.23.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260820222146-c27c302e5fc3 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)
