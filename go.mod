// Limes — budgeted, delegable, individually revocable capabilities for
// agentic workloads.
//
// This module deliberately depends on NOTHING that ties it to object storage
// or to a database: if a storage SDK or a database driver ever appears in the
// resolved graph, isolation_test.go fails `task verify-all`.
//
// Persistence is the consumer's concern — the module publishes Store,
// UsageStore[TX] and KeyResolver contracts and nothing more.
module github.com/oleg-tkachuk/limes

go 1.27.0

require (
	github.com/biscuit-auth/biscuit-go/v2 v2.2.0
	github.com/google/uuid v1.6.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
)
