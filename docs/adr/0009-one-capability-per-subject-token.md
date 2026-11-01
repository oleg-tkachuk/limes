# ADR-0009: One capability per subject token, on request

**Status:** Accepted
**Date:** 2026-10-29
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0003](0003-oauth-token-exchange.md)

## Context

An `exchange.Exchanger` issues a new capability on every exchange, each with
ceilings of its own. A subject token can be exchanged again and again until
it expires.

That is right for a client that exchanges once and keeps the result. It is
wrong for a service that exchanges where it receives the token: an MCP
server that exchanges per request, or once per replica. Each exchange
multiplies the request ceiling the policy meant for the token. A consumer
can only prevent this by building its own store keyed on the subject
token's `jti`, with a lock across replicas.

## Decision

1. **An opt-in mode.** `exchange.Config.Exchanges`, a `limes.ExchangeStore`,
   makes the Exchanger issue one capability per subject token: per its
   issuer and `jti`, within the tenant the policy names. Without it, nothing
   changes.

2. **The store records a mapping, never a token.** `limes.ExchangeStore`
   holds `(tenant, issuer, jti) → capability id` and nothing else.
   - `ExchangedFor` reads the mapping.
   - `ClaimExchange` records it unless the key holds one already. Concurrent
     claims of one key, from any replica, converge on one id.
   - The mapping outlives its capability: it is kept until the subject
     token expires (`ExchangeKey.TokenExpiresAt`), and Purge or an erase
     removes it only once that is past their retention. A token whose
     capability was purged is refused, not exchanged afresh.

3. **The token is signed again from the record.** `Issuer.Reissue` reads
   the capability on record, refuses it once it is expired or revoked, and
   signs it again. It records nothing.
   - The claims come from the record. Ed25519 is deterministic, so under the
     same key the second exchange returns the first exchange's exact token.
     Nothing stored can be replayed as a bearer, because no token is stored.
   - After a key rotation, the token is the same capability under the new
     key.

4. **The order of an exchange in this mode:**
   - verify the subject token;
   - refuse one without a `jti` (`ErrInvalidGrant`);
   - check the proof;
   - ask the policy, which names the tenant;
   - look the key up.

   **On a hit**, the recorded capability is reissued. The exchange refuses:
   - one that is revoked or expired (`ErrInvalidGrant`). A revoked exchange
     stays revoked: it never mints again;
   - one bound to another key than this proof's, or bound when this request
     is bearer, or the other way round (`ErrInvalidGrant`);
   - one whose audience is not within this request's targets
     (`ErrInvalidTarget`).

   **On a miss**, the exchange issues, then claims. When the claim loses a
   race, the winner's capability is reissued. The loser's own capability is
   left to expire: its token never left the process, so nothing can present
   it, and the Exchanger holds no store it could revoke through.

5. **The lifetime defaults to the subject token's.** In this mode a policy
   that names no TTL gets the subject token's remaining lifetime, not
   `DefaultTTL`. Under strict "one capability per token", a 15-minute
   capability would leave the rest of the token's life refused. A policy may
   still shorten it, and accepts that refusal.

6. **The contract lives in the root module,** beside `IssuanceStore` and
   `RecordReader`. pgstore implements it without importing `exchange`, so it
   links no JOSE library. `memstore` and `pgstore` implement it.
   - `storetest` checks it when `Env.Exchanges` is set.
   - `exchangetest` checks the mode when its Env names an `ExchangeStore`.

## Consequences

- **Replicas must agree on the key.** A capability bound by DPoP stays
  bound to the first exchange's key. Replicas that exchange one token must
  share that key, or exchange as bearer inside one trust domain.
- **The policy runs on every exchange, hits included,** since it names the
  tenant the key is scoped by. A policy with side effects runs again.
- **A losing race leaves an orphan record** that expires on its own and is
  never presented.
- **`Issuer.Reissue` signs any live capability on record.** Whoever holds
  the Issuer could already issue one; Reissue gives that holder nothing new.
