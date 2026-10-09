// The module's developer tools, run with `go tool -modfile=tools/go.mod`.
// A module of their own so they stay out of the graph of every consumer of
// limes: a tool directive in the root go.mod would put its requirements there.
module github.com/oleg-tkachuk/limes/tools

go 1.27.0

toolchain go1.27.2

tool (
	github.com/rhysd/actionlint/cmd/actionlint
	golang.org/x/vuln/cmd/govulncheck
)

require (
	github.com/bmatcuk/doublestar/v4 v4.10.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.21 // indirect
	github.com/mattn/go-shellwords v1.0.12 // indirect
	github.com/rhysd/actionlint v1.7.12 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.3 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/telemetry v0.0.0-20260908163034-4bcc4b2ee518 // indirect
	golang.org/x/tools v0.50.0 // indirect
	golang.org/x/vuln v1.8.0 // indirect
)
