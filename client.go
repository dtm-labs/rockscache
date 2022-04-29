package rockscache

import (
	"math/rand"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/lithammer/shortuuid"
	"golang.org/x/sync/singleflight"
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
	// RandomExpireAdjustment is the random adjustment for the expire time. default 0.1
	// if the expire time is set to 600s, and this value is set to 0.1, then the actual expire time will be 540s - 600s
	// solve the problem of cache avalanche.
	RandomExpireAdjustment float32
	// CacheReadDisabled is the flag to disable read cache. default is false
	// when redis is down, set this flat to downgrade.
	DisableCacheRead bool
	// CacheDeleteDisabled is the flag to disable delete cache. default is false
	// when redis is down, set this flat to downgrade.
	DisableCacheDelete bool
	// StrongConsistency is the flag to enable strong consistency. default is false
	// if enabled, the Fetch result will be consistent with the db result, but performance is bad.
	StrongConsistency bool
}

// NewDefaultOptions return default options
func NewDefaultOptions() Options {
	return Options{Delay: 10, EmptyExpire: 60, LockExpire: 3, RandomExpireAdjustment: 0.1}
}

// Client delay client
type Client struct {
	rdb     *redis.Client
	Options Options
	group   singleflight.Group
}

// NewClient new a delay client
func NewClient(rdb *redis.Client, options Options) *Client {
	if options.Delay == 0 || options.LockExpire == 0 {
		panic("cache options error: Delay and LockExpire should not be 0, you should call NewDefaultOptions() to get default options")
	}
	return &Client{rdb: rdb, Options: options}
}

// DelayDelete a key, the key will expire after delay time.
func (c *Client) DelayDelete(key string) error {
	if c.Options.DisableCacheDelete {
		return nil
	}
	debugf("delete: key=%s", key)
	_, err := callLua(c.rdb, ` --  delete
local v = redis.call('HGET', KEYS[1], 'value')
if v == false then
	return
end
redis.call('HSET', KEYS[1], 'lockUtil', ARGV[1])
redis.call('HDEL', KEYS[1], 'lockOwner')
redis.call('EXPIRE', KEYS[1], ARGV[2])
	`, []string{key}, []interface{}{time.Now().Add(-1 * time.Second).Unix(), c.Options.Delay})
	return err
}

// Fetch returns the value store in cache indexed by the key.
// If the key doest not exists, call fn to get result, store it in cache, then return.
func (c *Client) Fetch(key string, expire int, fn func() (string, error)) (string, error) {
	ex := expire - c.Options.Delay - int(rand.Float32()*c.Options.RandomExpireAdjustment*float32(expire))
	v, err, _ := c.group.Do(key, func() (interface{}, error) {
		if c.Options.DisableCacheRead {
			return fn()
		} else if c.Options.StrongConsistency {
			return c.strongFetch(key, ex, fn)
		}
		return c.weakFetch(key, ex, fn)
	})
	return v.(string), err
}

func (c *Client) weakFetch(key string, expire int, fn func() (string, error)) (string, error) {
	debugf("weakFetch: key=%s", key)
	owner := shortuuid.New()
	redisGet := func() ([]interface{}, error) {
		res, err := callLua(c.rdb, ` -- weakFetch
		local v = redis.call('HGET', KEYS[1], 'value')
		local lu = redis.call('HGET', KEYS[1], 'lockUtil')
		if lu ~= false and tonumber(lu) < tonumber(ARGV[1]) or lu == false and v == false then
			redis.call('HSET', KEYS[1], 'lockUtil', ARGV[1])
			redis.call('HSET', KEYS[1], 'lockOwner', ARGV[2])
			return { v, 'LOCKED' }
		end
		return {v, lu}
		`, []string{key}, []interface{}{now() + int64(c.Options.LockExpire), owner})
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
		if err != nil || result == "" && c.Options.EmptyExpire == 0 {
			return "", err
		}
		if result == "" {
			expire = c.Options.EmptyExpire
		}
		_, err = callLua(c.rdb, `-- weakFetch set
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
	debugf("strongFetch: key=%s", key)
	owner := shortuuid.New()
	redisGet := func() ([]interface{}, error) {
		res, err := callLua(c.rdb, ` -- strongFetch
		local v = redis.call('HGET', KEYS[1], 'value')
		local lu = redis.call('HGET', KEYS[1], 'lockUtil')

		if lu ~= false and tonumber(lu) < tonumber(ARGV[1]) -- delay deleted
		 or lu == false and v == false then -- empty
			redis.call('HSET', KEYS[1], 'lockUtil', ARGV[1])
			redis.call('HSET', KEYS[1], 'lockOwner', ARGV[2])
			return { v, 'LOCKED' }
		end
		return {v, lu}
		`, []string{key}, []interface{}{now() + int64(c.Options.LockExpire), owner})
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
	if err != nil || result == "" && c.Options.EmptyExpire == 0 {
		return "", err
	}
	if result == "" {
		expire = c.Options.EmptyExpire
	}
	_, err = callLua(c.rdb, `-- strongFetch Set
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
