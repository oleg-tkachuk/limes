# ADR-0021: Observing the issuer and the verifier

**Status:** Accepted
**Date:** 2026-11-06
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0001](0001-architecture-before-1-0.md), [ADR-0012](0012-error-codes.md)

## Context

The issuer mints every token and the verifier checks every request, yet
before v0.168.0 neither said what it did: a refusal reached the caller as an
error and went no further, and a service that wanted an audit trail or a
refusal rate wrapped each method itself. The hook has to be fixed before
1.0, after which its shape cannot change:

- It must serve a log, metrics and an audit sink, each optional.
- It must be able to grow in 1.x: a field a later release learns to report
  (how long the call took, say) must reach every consumer without a break.
- The root module resolves no OpenTelemetry (`isolation_test.go`), so the
  hook cannot be an OTel API.
- Every event a component handles is logged; none runs silent.

## Decision

1. **`IssuerConfig.Logger` and `VerifierConfig.Logger`, a `*slog.Logger`.**
   Nil logs to `slog.Default()`, resolved at each call. The issuer logs a
   token minted at INFO; the verifier a token accepted at DEBUG, a refusal
   before the signature verified at DEBUG, one after at WARN; either a
   failure of its own at ERROR. A capability is named by its id; no line
   holds a token, a key or a proof.
2. **`IssuerConfig.Events` and `VerifierConfig.Events`, structs of optional
   callbacks**: `IssuerEvents` with `OnIssued`, `OnRefused` and `OnFailed`;
   `VerifierEvents` with `OnVerified`, `OnRefused` and `OnFailed`. Each
   takes the context and one event struct (`IssuedEvent`,
   `IssuerErrorEvent`, `VerifiedEvent`, `VerifierErrorEvent`). Each outcome
   calls exactly one callback; a nil one drops its events, so a consumer
   that must see every call sets them all.
3. **Delivery is synchronous**: on the caller's goroutine, after the line
   is logged and before the method returns. A callback must not block and
   must not panic; neither is recovered.
4. **The event structs may gain fields in 1.x.** A callback reads the
   fields it knows; one added later breaks no consumer
   ([compatibility.md](../compatibility.md)). A callback field may be added
   to `IssuerEvents` or `VerifierEvents` too, nil by default.
5. **Every event carries `Duration`**, the call's elapsed time from its
   entry to its outcome on the monotonic clock, and every line carries it
   as `duration`. It is measured whether or not anything reads it: one
   `time.Now` per call, which allocates nothing; whether the line is logged
   depends on the outcome, which is not known at entry.

## Options considered

### A struct of optional callbacks and a logger (chosen)

**Pros:** grows in 1.x by adding fields, to the hooks and to the events;
needs no dependency; a consumer sets only what it reads. **Cons:** each
callback is independent, so a consumer that sets one and not the others
misses events silently.

### An `Observer` interface

`ObserveIssued(ctx, IssuedEvent)` and the like, one interface per side.

**Pros:** one value to set; a type that implements it is checked to handle
every outcome. **Cons:** an interface cannot gain a method after 1.0
without breaking every implementation, so a new kind of event would need a
second interface and a type assertion. Optional fields grow freely.

### OpenTelemetry in the root module

Spans and metrics emitted by the issuer and verifier themselves.

**Pros:** no adapter to write. **Cons:** the root module resolves no
OpenTelemetry (`isolation_test.go`); a consumer that runs none would
resolve it anyway. The otelmetric module exists to keep that dependency
out of limes's own graph.

### Asynchronous delivery

Events queued and handed to the callbacks from a goroutine of the
issuer's.

**Pros:** a slow callback does not delay the request. **Cons:** a queue
loses events when it is full or the process exits, or else blocks the
request anyway; the callbacks see events out of order across goroutines;
and the library would own a goroutine, a buffer size and a shutdown. A
consumer that wants a queue puts one behind its callback, sized for its own
load.

### Logging off by default

A nil Logger logging nothing, as a library often does.

**Pros:** a service that configured nothing sees no new lines. **Cons:**
every event a component handles is logged; a library silent until
configured is one whose refusals and failures nobody reads when they
matter. A consumer silences it explicitly instead (`docs/migration.md`,
v0.168.0).

## Consequences

- A service that configured no Logger logs to `slog.Default()` from
  v0.168.0 on: an INFO line per token minted, and the verifier's refusals
  and failures. `docs/migration.md` says how to silence it.
- An OpenTelemetry adapter — counters and a latency histogram from the
  events, `Duration` among them — belongs in otelmetric, once a limes
  release that carries these fields is the one otelmetric requires.
- An audit consumer sets every callback of a side; the doc comments of
  `IssuerEvents` and `VerifierEvents` and the guide say so.
- A callback runs on the request's path: its cost is the request's.
