# Examples

Small programs that show limes at work, one topic each. Every step is logged
to stdout as one line, and a step that does not do what the example says
stops it with a non-zero exit, so each example is also a check
(`task verify:examples`, `task test:examples:pgstore`).

Run them from this directory:

| Command | Shows |
|---------|-------|
| `go run ./issue-verify` | issuing a capability token, checking it as a service would on every request, the refusals for another resource, op or audience, and revocation |
| `go run ./delegation` | an orchestrator handing a narrower capability to a sub-agent, a wider one refused, and a revocation cascading down the chain |
| `go run ./metering` | counting requests and charging spend against the capability's and the tenant's ceilings: a direct charge, a hold settled at its actual cost, and the refusals at a limit |
| `go run ./pgstore` | the same over PostgreSQL, started in a container: the schema, row-level security per tenant, and a charge committed in one transaction with a write of the caller's own. Needs a running Docker |

The first three keep everything in memory (`memstore`). This directory is a
module of its own, so that the limes module never resolves a database driver;
its `replace` lines build it against the limes and pgstore beside it.

A line of output reads like this:

```
level=INFO msg="refused, as expected" example=issue-verify step="verify the revoked token" err="limes: revoked"
```
