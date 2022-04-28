package rockscache

import (
	"errors"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/lithammer/shortuuid"
)

// Optiions represents the options for rockscache client
type Options struct {
	// Delay is the delay delete time for keys that are tag deleted. unit seconds. default is 10s
	Delay int
	// EmptyExpire is the expire time for empty result. unit seconds. default is 60s
	EmptyExpire int
	// LockExpire is the expire time for the lock which is allocated when updating cache. unit seconds. default is 3s
	// should be set to the max of the underling data calculating time.
	LockExpire int
	// CacheReadDisabled is the flag to disable read cache. default is false
	// when redis is down, set this flat to downgrade.
	CacheReadDisabled bool
	// CacheDeleteDisabled is the flag to disable delete cache. default is false
	// when redis is down, set this flat to downgrade.
	CacheDeleteDisabled bool
	// StrongConsistency is the flag to enable strong consistency. default is false
	// if enabled, the Fetch result will be consistent with the db result, but performance is bad.
	StrongConsistency bool
}

// NewDefaultOptions return default options
func NewDefaultOptions() Options {
	return Options{Delay: 10, EmptyExpire: 60, LockExpire: 3}
}

// Client delay client
type Client struct {
	rdb     *redis.Client
	options Options
}

// NewClient new a delay client
func NewClient(rdb *redis.Client, options Options) (*Client, error) {
	if options.Delay == 0 || options.LockExpire == 0 {
		return nil, errors.New("cache options error: delay and lock expire should not be 0")
	}
	return &Client{rdb: rdb, options: options}, nil
}

// Delete tag a key deleted, then the key will expire after delay time.
func (c *Client) Delete(key string) error {
	if c.options.CacheDeleteDisabled {
		return nil
	}
	debugf("delay.Delete: key=%s", key)
	_, err := callLua(c.rdb, ` --  delay.Delete
local v = redis.call('HGET', KEYS[1], 'value')
if v == false then
	return
end
redis.call('HSET', KEYS[1], 'lockUtil', ARGV[1])
redis.call('HDEL', KEYS[1], 'lockOwner')
redis.call('EXPIRE', KEYS[1], ARGV[2])
	`, []string{key}, []interface{}{time.Now().Add(-1 * time.Second).Unix(), c.options.Delay})
	return err
}

// Fetch returns the value store in cache indexed by the key.
// If the key doest not exists, call fn to get result, store it in cache, then return.
func (c *Client) Fetch(key string, expire int, fn func() (string, error)) (string, error) {
	if c.options.CacheReadDisabled {
		return fn()
	}
	if c.options.StrongConsistency {
		return c.strongFetch(key, expire, fn)
	}
	return c.normalFetch(key, expire, fn)
}

func (c *Client) normalFetch(key string, expire int, fn func() (string, error)) (string, error) {
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
		`, []string{key}, []interface{}{now() + int64(c.options.LockExpire), owner})
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
			expire = c.options.EmptyExpire
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

func (c *Client) strongFetch(key string, expire int, fn func() (string, error)) (string, error) {
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
		`, []string{key}, []interface{}{now() + int64(c.options.LockExpire), owner})
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
		expire = c.options.EmptyExpire
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
