-- Records one DPoP proof id in its holder's sorted set and reports whether it
-- was already there: 1 for a replay or a refusal, 0 for a new id.
--
-- KEYS[1]  the holder's sorted set
-- ARGV[1]  the member: the key's thumbprint and the proof's id
-- ARGV[2]  now, in Unix milliseconds of the caller's clock
-- ARGV[3]  when the id lapses, in Unix milliseconds of the caller's clock
-- ARGV[4]  how many live ids the holder may have
--
-- Scores are expiries on the caller's clock, so what is live is decided here
-- and never by the server's clock; the key's TTL, a duration, only collects a
-- holder that has gone quiet.
local key = KEYS[1]
local member = ARGV[1]
local now = tonumber(ARGV[2])
local expires = tonumber(ARGV[3])
local share = tonumber(ARGV[4])

redis.call('ZREMRANGEBYSCORE', key, '-inf', now)
if redis.call('ZSCORE', key, member) then
	return 1
end
if redis.call('ZCARD', key) >= share then
	return 1
end
local ttl = expires - now
if ttl <= 0 then
	-- Lapsed already: nothing could replay it, so nothing is kept.
	return 0
end
-- ARGV[3] as sent: a Lua number may print in exponent form.
redis.call('ZADD', key, ARGV[3], member)
if redis.call('PTTL', key) < ttl then
	redis.call('PEXPIRE', key, ttl)
end
return 0
