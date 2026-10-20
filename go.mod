// Limes — budgeted, delegable, individually revocable capabilities for
// agentic workloads.
//
// This module deliberately depends on NOTHING that ties it to object storage,
// a database, Redis or a telemetry backend: if a storage SDK, a database
// driver, a Redis client or OpenTelemetry ever appears in the resolved graph,
// isolation_test.go fails `task verify-all`.
//
// Persistence is the consumer's concern — the module publishes the storage
// contracts (Store, meter.UsageStore, KeyResolver and the optional ones) and
// no implementation beyond memstore. The PostgreSQL one, pgstore, is a module of
// its own (github.com/oleg-tkachuk/limes/pgstore), so its driver stays out of
// this graph.
module github.com/oleg-tkachuk/limes

go 1.26.0

toolchain go1.27.2

require (
	// The upstream of encoding/json/v2, which Go 1.26 still keeps behind
	// GOEXPERIMENT; replace it with the standard library once the go
	// directive reaches the release that ships it.
	github.com/go-json-experiment/json v0.0.0-20260820222146-c27c302e5fc3
	github.com/google/uuid v1.6.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/eclipse-biscuit/biscuit-go/v2 v2.2.2
	golang.org/x/text v0.43.0
)
