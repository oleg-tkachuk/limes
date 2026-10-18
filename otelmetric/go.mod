// OpenTelemetry instruments for a limes.UsageStore. A module of its own so
// that limes itself never resolves an OpenTelemetry dependency.
module github.com/oleg-tkachuk/limes/otelmetric

go 1.26.0

toolchain go1.27.2

require (
	github.com/google/uuid v1.6.0
	github.com/oleg-tkachuk/limes v0.98.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/eclipse-biscuit/biscuit-go/v2 v2.2.2 // indirect
	github.com/go-json-experiment/json v0.0.0-20260820222146-c27c302e5fc3 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
