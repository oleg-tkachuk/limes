// The module's developer tools, run with `go tool -modfile=tools/go.mod`.
// A module of their own so they stay out of the graph of every consumer of
// limes: a tool directive in the root go.mod would put its requirements there.
module github.com/oleg-tkachuk/limes/tools

go 1.27.0

toolchain go1.27.2

tool (
	github.com/rhysd/actionlint/cmd/actionlint
	golang.org/x/exp/cmd/apidiff
	golang.org/x/perf/cmd/benchstat
	golang.org/x/vuln/cmd/govulncheck
)

require (
	github.com/aclements/go-moremath v0.0.0-20210112150236-f10218a38794 // indirect
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
	golang.org/x/exp v0.0.0-20261011100332-faecfb24eb49 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/perf v0.0.0-20261009192801-be2c69fb417e // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)
