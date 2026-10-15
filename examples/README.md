# Examples

Small programs that show limes at work, one topic each. Each one tells what
it does step by step on stdout, in plain words, with the values that matter
below each step. A step that does not do what the example says stops it with
a non-zero exit, so each example is also a check (`task verify:examples`,
`task test:examples:pgstore`).

Run them from this directory:

| Command | Shows |
|---------|-------|
| `go run ./quickstart` | the README's walkthrough in one program: issue, authorize, delegate, meter and revoke |
| `go run ./issue-verify` | issuing a capability token, checking it as a service would on every request, the refusals for another resource, op or audience, and revocation |
| `go run ./delegation` | an orchestrator handing a narrower capability to a sub-agent, a wider one refused, and a revocation cascading down the chain |
| `go run ./metering` | counting requests and charging spend against the capability's and the tenant's ceilings: a direct charge, a hold settled at its actual cost, and the refusals at a limit |
| `go run ./pgstore` | the same over PostgreSQL, started in a container: the schema, row-level security per tenant, and a charge committed in one transaction with a write of the caller's own. Needs a running Docker |

All but `pgstore` keep everything in memory (`memstore`). This directory is a
module of its own, so that the limes module never resolves a database driver;
its `replace` lines build it against the limes and pgstore beside it.

Each step is marked `•` when it is done, `✓` for a refusal the example sets
out to show, and `✗` when something did not go as described:

```
• issued a capability token to the agent: what it may do travels in the token
    capability       01a13fe2-f383-77d7-b40d-852485955220
    agent            research-agent
    operations       [get]
    resources under  [corpus/public/]
    expires          2026-10-15T14:21:40Z
    token size       746 bytes
✓ refused, as expected: verify the revoked token
    error  limes: revoked
```
