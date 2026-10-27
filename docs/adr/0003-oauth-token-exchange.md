# ADR-0003: Bridge OAuth to capabilities by token exchange

**Status:** Proposed
**Date:** 2026-10-27
**Deciders:** Oleg Tkachuk

## Context

An MCP server is an OAuth resource server. The MCP authorization
specification (2026-07-28) fixes how its clients get a token:

- The server publishes RFC 9728 protected resource metadata, and names its
  authorization servers there. A 401 carries `resource_metadata` in its
  `WWW-Authenticate` challenge.
- The client discovers that authorization server (RFC 8414 or OpenID
  Connect discovery), registers (client ID metadata documents, or
  pre-registration), and runs authorization code with PKCE. It sends
  `resource` (RFC 8707) naming the MCP server.
- The server accepts only tokens issued for it by its authorization server,
  and "MUST NOT accept or transit any other tokens".

So a standard MCP client holds whatever its authorization server issues.
Usually that is an identity provider's access token, which says who the user
is and which client acts, and nothing about operations, resources, budgets
or revocation. limes says all of that, but it is not an authorization server
(`docs/standards.md`, "Not adopted").

A downstream MCP server wants both: OAuth at its edge, as clients expect,
and capabilities behind it, for its tools and the agents it calls. Today
each such server writes the glue itself: verify the identity provider's
token, decide what the caller may do, call `Issuer.Issue`. RFC 8693 (Token
Exchange) is the standard shape of that glue.

The question for now, before 1.0, is narrower: does the root module's API
carry such a bridge, or does the bridge need a change that the freeze would
then put off until /v2?

## Decision

1. **Where the exchange happens.** The MCP server, or a gateway in front of
   it, exchanges the token it received for a capability, on its own side.
   Clients keep the flow the specification gives them. The identity provider
   stays the authorization server in the protected resource metadata. The
   capability is used behind the server: for its tool calls, and for the
   agents and services it calls on.
2. **A nested module, `exchange`.** It has a version line of its own, and
   the root module gains no dependency. It provides:
   - an `Exchanger`: subject token in, capability out, in process. This is
     the primary form: the token the server received never leaves it;
   - an `http.Handler` for the RFC 8693 token endpoint, for a gateway or a
     token service shared within the server's own trust domain. It
     authenticates its clients, and accepts a subject token only when the
     token's audience is the client exchanging it, so a server presents
     only a token issued to itself. It answers errors as RFC 6749 §5.2 and
     RFC 8707 (`invalid_target`) define them;
   - helpers for the RFC 9728 protected resource metadata document, and a
     minimal RFC 8414 document for that token endpoint.
3. **Policy is the consumer's.** One callback turns a verified subject (its
   issuer, subject, client, scopes), the `resource` and any RFC 9396
   `authorization_details` into an `IssueRequest`, or a refusal. The module
   ships no default policy: a default would grant something nobody chose.
   The policy is the confused-deputy boundary. The exchanging server is
   trusted to ask only for what the subject token authorises, and the
   module holds it to that where it can: the capability's audience is the
   requested `resource`, its lifetime ends no later than the subject
   token's, and a refusal is logged with the subject and the client.
4. **Subject tokens.** Two kinds are accepted:
   - an identity provider's JWT access token, verified against its JWKS with
     RFC 9068's checks (`typ`, `iss`, `aud`, `exp`, `client_id`) behind a
     `SubjectVerifier` interface, whose default implementation uses jwx;
   - a limes capability, verified by `Verifier.Verify` and narrowed by
     `Issuer.Delegate`, for a sub-agent's token.
5. **Proof of possession.** The capability is bound to the client's key
   when the request carries a DPoP proof (RFC 9449 §5), and the response's
   `token_type` is then `DPoP`. A DPoP-bound subject token needs a proof by
   its own key.

## Options considered

### Option A: exchange on the resource server's side (chosen)

| Dimension | Assessment |
|---|---|
| Fit with MCP clients | Full: they keep the specified flow |
| Size | One module: verification, policy hook, token endpoint, metadata |
| Root API | Unchanged; two additive gaps, below |
| Security | Narrow: limes never handles a login, a client registration or a redirect |

**Pros:** no authorization server to run; the capability carries the limits.
**Cons:** the client's own token stays the identity provider's, so limits
apply behind the server, not at the client.

### Option B: limes as the MCP server's authorization server

The bridge would be named in the protected resource metadata. It would run
authorization code with PKCE, client ID metadata documents and RFC 9207,
delegating login to the identity provider, and issue capabilities as access
tokens.

| Dimension | Assessment |
|---|---|
| Fit with MCP clients | Full, and the client holds the capability |
| Size | A full OAuth 2.1 authorization server |
| Root API | Additive: the challenge must carry `resource_metadata` and `scope` |
| Security | Wide: logins, redirects, client registration, code theft |

**Pros:** limits apply at the client.
**Cons:** it makes limes, or each service adopting it, run an authorization
server: the most attacked kind of component. Kept open: nothing in Option A
precludes it.

### Option C: clients exchange the token themselves

**Cons:** no MCP client does. The specification gives them no such step,
so it serves custom clients only.

## Trade-off analysis

Option A takes the standard flow as it is and puts limes where its
guarantees matter, at the calls that spend budget and reach resources. Option
B buys enforcement at the client at the price of running an authorization
server. The force that decides is the attack surface. A future need for B
does not have to wait for /v2: its root changes are additive.

## Does the root API carry it?

Each need of the module, and what the root module already gives it:

| Need | Root API | Verdict |
|---|---|---|
| Mint a capability for an outside identity | `Issuer.Issue`: `IssuedBy`, `Subject`, `Audience`, `Caveats`, `TTL`, `NotBefore`, `ConfirmationJKT` | Enough |
| Tenant from the identity provider's organisation | `Principal.TenantID` is a UUID; the policy maps an organisation to one | Enough, with a mapping |
| Audience from `resource` (RFC 8707) | An audience is any non-empty string | Enough |
| Operations from `scope` or `authorization_details` | Built-in and namespaced `Op`s, resource prefixes, ceilings | Enough |
| The client behind the token | `AgentPrincipal.MCPClient` | Enough |
| Principal kinds: the user, the agent acting for them, the exchanging service as `IssuedBy` | `PrincipalTypeUser`, `PrincipalTypeAgent`, `PrincipalTypeService`; the set is closed and on the wire | Enough: each party of the exchange is one of the three |
| Narrow a capability given as the subject token | `Verifier.Verify`, `Issuer.Delegate` | Enough |
| A DPoP-bound subject token | `Verifier.Verify` with a proof over it; `dpop.Verifier.Check` with the identity provider's `cnf.jkt` | Enough |
| `token_type`, `expires_in` | `Capability.ConfirmationJKT`, `Capability.ExpiresAt` | Enough |
| Metadata: JWKS, DPoP algorithms | `jwks.NewHandler`, `dpop.SigningAlgorithms` | Enough |
| **Bind to a key proven at the token endpoint** | `dpop.Verifier` checks a proof against a token (`ath`) and a known `JKT`. A token request has neither, and the key is the proof's own | **Gap, additive:** a method on `*dpop.Verifier` that checks a proof with no token and returns its thumbprint |
| **Option B only: `resource_metadata` and `scope` in challenges** | `Verifier.ErrorResponse` writes `realm`, `error`, `algs` | **Gap, additive:** a `VerifierConfig` field |

Neither gap changes an existing identifier or a method of an interface a
consumer implements, so either can land in a 1.x minor. The root API carries
the bridge; 1.0 does not wait for it.

The first gap is closed when the module is built, so that its shape follows
its one caller. It must reuse the proof checks `dpop.Verifier` already makes
(`typ`, algorithm, `jwk`, `htm`, `htu`, `iat`, nonce, replay), not repeat
them.

## Consequences

- An MCP server gets capabilities behind standard OAuth without running an
  authorization server.
- `docs/standards.md` moves RFC 8693, RFC 8707, RFC 9396 (as a request
  parameter only), RFC 9728 and RFC 8414 out of "Not adopted" when the
  module ships. The reason they were dropped, that nothing read them, no
  longer holds there.
- Capabilities do not become RFC 9068 tokens: the exchange issues
  `limes-cap-v6+jwt`, with `issued_token_type` naming it by a URI of the
  module's own.
- To revisit: Option B, if limits must hold at the client; revocation of
  every capability exchanged for one identity-provider session, for which
  the module would record the session with each capability.

## Action items

1. [ ] Add to `dpop.Verifier` a check of a token-endpoint proof (RFC 9449
   §5) that returns the proof key's thumbprint, with the same refusals and
   replay recording as `Check` and `Record`.
2. [ ] Module `exchange`: `SubjectVerifier` (jwx, RFC 9068 checks), the
   policy callback, `Exchanger`, and the RFC 8693 `http.Handler`, with the
   storetest-style contract tests and logging of every grant and refusal.
3. [ ] RFC 9728 and RFC 8414 document helpers; an example MCP server in
   `examples` that exchanges a token from a test identity provider.
4. [ ] Register the module: release rules, CI group, seed tag.
