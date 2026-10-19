-- A capability's quota of Biscuit copies counts the copies of every
-- capability delegated from it. Each usage row keeps that count, as it keeps
-- the requests and spend of its subtree, so admitting a copy reads the
-- chain's rows rather than walking the subtree. A copy stays counted under
-- the ancestors once its capability's counters are dropped.
ALTER TABLE limes.usage
    ADD COLUMN copies_under bigint NOT NULL DEFAULT 0 CHECK (copies_under >= 0);

-- Every copy counted so far, under its capability and each ancestor on
-- record. Each step goes to a parent of smaller depth, as every parent the
-- store records is, so the walk ends whatever parent_id a row was given by
-- hand.
WITH RECURSIVE chain (id) AS (
    SELECT capability_id FROM limes.copy_usage
  UNION ALL
    SELECT p.id
    FROM chain
    JOIN limes.capabilities c ON c.id = chain.id
    JOIN limes.capabilities p ON p.id = c.parent_id AND p.depth < c.depth
)
UPDATE limes.usage u
SET copies_under = n.copies
FROM (SELECT id, count(*) AS copies FROM chain GROUP BY id) n
WHERE u.capability_id = n.id;
