---- tern: disable-tx ----
-- Purge drops the exchanges whose subject tokens expired. Built
-- CONCURRENTLY, holding no writes (CONTRIBUTING.md); dropped first, so a
-- build that failed runs again.
DROP INDEX CONCURRENTLY IF EXISTS limes.exchanges_token_expires_at;
CREATE INDEX CONCURRENTLY exchanges_token_expires_at ON limes.exchanges (token_expires_at);
