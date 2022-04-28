package rockscache

import (
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/lithammer/shortuuid"
)

// Client delay client
type Client struct {
	rdb *redis.Client
	// Delay is the delay delete time. unit seconds. default is 10s
	Delay int
	// EmptyExpire is the expire time for empty result. unit seconds. default is 60s
	EmptyExpire int
	// LockExpire is the expire time for lock. unit seconds. default is 3s
	LockExpire int
}

// NewClient new a delay client
func NewClient(rdb *redis.Client) *Client {
	return &Client{rdb: rdb, Delay: 10, EmptyExpire: 60, LockExpire: 3}
}

// Delete delay delete a key
func (c *Client) Delete(key string) error {
	debugf("delay.Delete: key=%s", key)
	_, err := callLua(c.rdb, ` --  delay.Delete
local v = redis.call('HGET', KEYS[1], 'value')
if v == false then
	return
end
redis.call('HSET', KEYS[1], 'lockUtil', ARGV[1])
redis.call('HDEL', KEYS[1], 'lockOwner')
redis.call('EXPIRE', KEYS[1], ARGV[2])
	`, []string{key}, []interface{}{time.Now().Add(-1 * time.Second).Unix(), c.Delay})
	return err
}

// Obtain obtain a key. If value is empty, call fn to get value.
func (c *Client) Obtain(key string, expire int, fn func() (string, error)) (string, error) {
	debugf("delay.Obtain: key=%s", key)
	owner := shortuuid.New()
	redisGet := func() ([]interface{}, error) {
		res, err := callLua(c.rdb, ` -- delay.Obtain
		local v = redis.call('HGET', KEYS[1], 'value')
		local lu = redis.call('HGET', KEYS[1], 'lockUtil')
		if lu ~= false and tonumber(lu) < tonumber(ARGV[1]) or lu == false and v == false then
			redis.call('HSET', KEYS[1], 'lockUtil', ARGV[1])
			redis.call('HSET', KEYS[1], 'lockOwner', ARGV[2])
			return { v, 'LOCKED' }
		end
		return {v, lu}
		`, []string{key}, []interface{}{now() + int64(c.LockExpire), owner})
		if err != nil {
			return nil, err
		}
		return res.([]interface{}), nil
	}
	r, err := redisGet()
	debugf("r is: %v", r)

	for err == nil && r[0] == nil && r[1].(string) != "LOCKED" {
		debugf("empty result for %s lock by other, so sleep 1s", key)
		time.Sleep(1 * time.Second)
		r, err = redisGet()
	}
	if err != nil {
		return "", err
	}
	if r[1] != "LOCKED" {
		return r[0].(string), nil
	}
	getNew := func() (string, error) {
		result, err := fn()
		if err != nil {
			return "", err
		}
		if result == "" {
			expire = c.EmptyExpire
		}
		_, err = callLua(c.rdb, `-- delay.Set
	local o = redis.call('HGET', KEYS[1], 'lockOwner')
	if o ~= ARGV[2] then
			return
	end
	redis.call('HSET', KEYS[1], 'value', ARGV[1])
	redis.call('HDEL', KEYS[1], 'lockUtil')
	redis.call('HDEL', KEYS[1], 'lockOwner')
	redis.call('EXPIRE', KEYS[1], ARGV[3])
	`, []string{key}, []interface{}{result, owner, expire})
		return result, err
	}
	if r[0] == nil {
		return getNew()
	}
	go getNew()
	return r[0].(string), nil
}

// StrongObtain obtain a key. If value is empty, call fn to get value.
func (c *Client) StrongObtain(key string, expire int, fn func() (string, error)) (string, error) {
	debugf("delay.Obtain: key=%s", key)
	owner := shortuuid.New()
	redisGet := func() ([]interface{}, error) {
		res, err := callLua(c.rdb, ` -- delay.Obtain
		local v = redis.call('HGET', KEYS[1], 'value')
		local lu = redis.call('HGET', KEYS[1], 'lockUtil')

		if lu ~= false and tonumber(lu) < tonumber(ARGV[1]) -- delay deleted
		 or lu == false and v == false then -- empty
			redis.call('HSET', KEYS[1], 'lockUtil', ARGV[1])
			redis.call('HSET', KEYS[1], 'lockOwner', ARGV[2])
			return { v, 'LOCKED' }
		end
		return {v, lu}
		`, []string{key}, []interface{}{now() + int64(c.LockExpire), owner})
		if err != nil {
			return nil, err
		}
		return res.([]interface{}), nil
	}
	r, err := redisGet()
	debugf("r is: %v", r)

	for err == nil && r[1] != nil && r[1] != "LOCKED" { // locked by other
		debugf("lock by other, so sleep 1s")
		time.Sleep(1 * time.Second)
		r, err = redisGet()
	}
	if err != nil {
		return "", err
	}
	if r[1] == nil { // normal value
		return r[0].(string), nil
	}

	result, err := fn()
	if err != nil {
		return "", err
	}
	if result == "" {
		expire = c.EmptyExpire
	}
	_, err = callLua(c.rdb, `-- delay.Set
	local o = redis.call('HGET', KEYS[1], 'lockOwner')
	if o ~= ARGV[2] then
			return
	end
	redis.call('HSET', KEYS[1], 'value', ARGV[1])
	redis.call('HDEL', KEYS[1], 'lockUtil')
	redis.call('HDEL', KEYS[1], 'lockOwner')
	redis.call('EXPIRE', KEYS[1], ARGV[3])
	`, []string{key}, []interface{}{result, owner, expire})
	return result, err
}
