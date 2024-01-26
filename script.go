package rockscache

import "github.com/redis/go-redis/v9"

var (
	deleteScript = redis.NewScript(`
redis.call('HSET', KEYS[1], 'lockUntil', 0)
redis.call('HDEL', KEYS[1], 'lockOwner')
redis.call('EXPIRE', KEYS[1], ARGV[1])`)

	getScript = redis.NewScript(`
local v = redis.call('HGET', KEYS[1], 'value')
local lu = redis.call('HGET', KEYS[1], 'lockUntil')
if lu ~= false and tonumber(lu) < tonumber(ARGV[1]) or lu == false and v == false then
	redis.call('HSET', KEYS[1], 'lockUntil', ARGV[2])
	redis.call('HSET', KEYS[1], 'lockOwner', ARGV[3])
	return { v, 'LOCKED' }
end
return {v, lu}`)

	setScript = redis.NewScript(`
local o = redis.call('HGET', KEYS[1], 'lockOwner')
if o ~= ARGV[2] then
		return
end
redis.call('HSET', KEYS[1], 'value', ARGV[1])
redis.call('HDEL', KEYS[1], 'lockUntil')
redis.call('HDEL', KEYS[1], 'lockOwner')
redis.call('EXPIRE', KEYS[1], ARGV[3])`)

	lockScript = redis.NewScript(`
local lu = redis.call('HGET', KEYS[1], 'lockUntil')
local lo = redis.call('HGET', KEYS[1], 'lockOwner')
if lu == false or tonumber(lu) < tonumber(ARGV[2]) or lo == ARGV[1] then
	redis.call('HSET', KEYS[1], 'lockUntil', ARGV[2])
	redis.call('HSET', KEYS[1], 'lockOwner', ARGV[1])
	return 'LOCKED'
end
return lo`)

	unlockScript = redis.NewScript(`
local lo = redis.call('HGET', KEYS[1], 'lockOwner')
if lo == ARGV[1] then
	redis.call('HSET', KEYS[1], 'lockUntil', 0)
	redis.call('HDEL', KEYS[1], 'lockOwner')
	redis.call('EXPIRE', KEYS[1], ARGV[2])
end`)

	getBatchScript = redis.NewScript(`
local rets = {}
for i, key in ipairs(KEYS)
do
	local v = redis.call('HGET', key, 'value')
	local lu = redis.call('HGET', key, 'lockUntil')
	if lu ~= false and tonumber(lu) < tonumber(ARGV[1]) or lu == false and v == false then
		redis.call('HSET', key, 'lockUntil', ARGV[2])
		redis.call('HSET', key, 'lockOwner', ARGV[3])
		table.insert(rets, { v, 'LOCKED' })
	else
		table.insert(rets, {v, lu})
	end
end
return rets`)

	setBatchScript = redis.NewScript(`
local n = #KEYS
for i, key in ipairs(KEYS)
do
	local o = redis.call('HGET', key, 'lockOwner')
	if o ~= ARGV[1] then
			return
	end
	redis.call('HSET', key, 'value', ARGV[i+1])
	redis.call('HDEL', key, 'lockUntil')
	redis.call('HDEL', key, 'lockOwner')
	redis.call('EXPIRE', key, ARGV[i+1+n])
end`)

	deleteBatchScript = redis.NewScript(`
for i, key in ipairs(KEYS) do
	redis.call('HSET', key, 'lockUntil', 0)
	redis.call('HDEL', key, 'lockOwner')
	redis.call('EXPIRE', key, ARGV[1])
end`)
)
