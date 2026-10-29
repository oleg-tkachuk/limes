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

go 1.27.0

toolchain go1.27.3

require golang.org/x/text v0.43.0
