-- A refund returns a charge only to the counters that counted it
-- (meter.Meter.Refund): DeleteUsage restarts a capability's counters, and
-- its copies', from zero, and a row restarted after a charge never counted
-- it. One sequence numbers the counter rows as they are created (born) and
-- the charges as they are taken (seq); a refund leaves a row born after the
-- charge. Rows already on record are taken as older than every charge
-- already taken, which are older than every row and charge to come: the
-- columns are added with constant defaults, which write no row, and only
-- then default to the sequence, which starts above them.
CREATE SEQUENCE limes.counter_seq AS bigint START WITH 2;

ALTER TABLE limes.usage ADD COLUMN born bigint NOT NULL DEFAULT 0;
ALTER TABLE limes.usage ALTER COLUMN born SET DEFAULT nextval('limes.counter_seq');

ALTER TABLE limes.copy_usage ADD COLUMN born bigint NOT NULL DEFAULT 0;
ALTER TABLE limes.copy_usage ALTER COLUMN born SET DEFAULT nextval('limes.counter_seq');

ALTER TABLE limes.charges ADD COLUMN seq bigint NOT NULL DEFAULT 1;
ALTER TABLE limes.charges ALTER COLUMN seq SET DEFAULT nextval('limes.counter_seq');
