// Node's jose, the most used JOSE library in JavaScript, verifies limes
// capability tokens through the issuer's JWKS, as an MCP server or gateway
// written in TypeScript would. The fixture is written by
// ../../node_fixture_test.go, which also checks that limes still verifies it.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { createLocalJWKSet, decodeProtectedHeader, jwtVerify, type JSONWebKeySet } from "jose";

interface Fixture {
  issuer: string;
  audience: string;
  subject: string;
  tenant: string;
  resource: string;
  verify_at: number;
  jwks: JSONWebKeySet;
  tokens: Record<string, string>;
}

const fixture = JSON.parse(readFileSync(new URL("fixture.json", import.meta.url), "utf8")) as Fixture;
// The two names of Ed25519 a limes signer writes, and the token type.
const ED25519 = "Ed25519";
const EDDSA = "EdDSA";
const ALGORITHMS = [ED25519, EDDSA];
const TOKEN_TYPE = "limes-cap-v6+jwt";
// MS_PER_SECOND turns the fixture's seconds into a Date.
const MS_PER_SECOND = 1000;
// A clock past every token's end.
const LATER_MS = 365 * 24 * 60 * 60 * MS_PER_SECOND;

const keys = createLocalJWKSet(fixture.jwks);
const at = new Date(fixture.verify_at * MS_PER_SECOND);
const verify = (token: string, audience = fixture.audience, currentDate = at) =>
  jwtVerify(token, keys, { issuer: fixture.issuer, audience, currentDate, typ: TOKEN_TYPE, algorithms: ALGORITHMS });

// jose matches a key's alg to the token's, so each key's entry names the
// alg of the tokens it signs (jwks.MarshalAlgorithms): under a JWKS naming
// Ed25519 for every key, jose finds no key for a token naming EdDSA.
test("jose finds no key for an EdDSA capability under an entry naming Ed25519", async () => {
  const jwks = structuredClone(fixture.jwks);
  for (const key of jwks.keys) {
    key.alg = ED25519;
  }
  await assert.rejects(
    jwtVerify(fixture.tokens.EdDSA, createLocalJWKSet(jwks), { issuer: fixture.issuer, audience: fixture.audience, currentDate: at }),
    { code: "ERR_JWKS_NO_MATCHING_KEY" },
  );
});

for (const alg of ALGORITHMS) {
  const token = fixture.tokens[alg];

  test(`jose verifies a capability naming ${alg}, and reads its claims`, async () => {
    assert.ok(token, `the fixture has no ${alg} token`);
    assert.equal(decodeProtectedHeader(token).alg, alg);
    const { payload } = await verify(token);
    assert.equal(payload.sub, fixture.subject);
    const principal = payload.limes_principal as { tenant_id: string; subject: string };
    assert.equal(principal.tenant_id, fixture.tenant);
    const caveats = payload.limes_caveats as { ops: string[]; resource_prefixes: string[] };
    assert.deepEqual(caveats.ops, ["get"]);
    assert.deepEqual(caveats.resource_prefixes, [fixture.resource]);
  });

  test(`jose refuses a ${alg} capability for another audience, expired or tampered`, async () => {
    await assert.rejects(verify(token, "another-api"), /aud/);
    await assert.rejects(verify(token, fixture.audience, new Date(at.getTime() + LATER_MS)), /exp/);
    const [header, payload, signature] = token.split(".");
    const claims = JSON.parse(Buffer.from(payload, "base64url").toString("utf8"));
    claims.sub = "intruder";
    const forged = [header, Buffer.from(JSON.stringify(claims)).toString("base64url"), signature].join(".");
    await assert.rejects(verify(forged), /signature/);
  });
}
