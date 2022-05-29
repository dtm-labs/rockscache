package rockscache

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/lithammer/shortuuid"
	"golang.org/x/sync/singleflight"
)

const locked = "LOCKED"

// Options represents the options for rockscache client
type Options struct {
	// Delay is the delay delete time for keys that are tag deleted. default is 10s
	Delay time.Duration
	// EmptyExpire is the expire time for empty result. default is 60s
	EmptyExpire time.Duration
	// LockExpire is the expire time for the lock which is allocated when updating cache. default is 3s
	// should be set to the max of the underling data calculating time.
	LockExpire time.Duration
	// LockSleep is the sleep interval time if try lock failed. default is 100ms
	LockSleep time.Duration
	// WaitReplicas is the number of replicas to wait for. default is 0
	// if WaitReplicas is > 0, it will use redis WAIT command to wait for TagDeleted synchronized.
	WaitReplicas int
	// WaitReplicasTimeout is the number of replicas to wait for. default is 3000ms
	// if WaitReplicas is > 0, WaitReplicasTimeout is the timeout for WAIT command.
	WaitReplicasTimeout time.Duration
	// RandomExpireAdjustment is the random adjustment for the expire time. default 0.1
	// if the expire time is set to 600s, and this value is set to 0.1, then the actual expire time will be 540s - 600s
	// solve the problem of cache avalanche.
	RandomExpireAdjustment float64
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
	return Options{
		Delay:                  10 * time.Second,
		EmptyExpire:            60 * time.Second,
		LockExpire:             3 * time.Second,
		LockSleep:              100 * time.Millisecond,
		RandomExpireAdjustment: 0.1,
		WaitReplicasTimeout:    3000 * time.Millisecond,
	}
}

// Client delay client
type Client struct {
	rdb     *redis.Client
	Options Options
	group   singleflight.Group
}

// NewClient return a new rockscache client
// for each key, rockscache client store a hash set,
// the hash set contains the following fields:
// value: the value of the key
// lockUtil: the time when the lock is released.
// lockOwner: the owner of the lock.
// if a thread query the cache for data, and no cache exists, it will lock the key before querying data in DB
func NewClient(rdb *redis.Client, options Options) *Client {
	if options.Delay == 0 || options.LockExpire == 0 {
		panic("cache options error: Delay and LockExpire should not be 0, you should call NewDefaultOptions() to get default options")
	}
	return &Client{rdb: rdb, Options: options}
}

// TagDeleted a key, the key will expire after delay time.
func (c *Client) TagDeleted(key string) error {
	if c.Options.DisableCacheDelete {
		return nil
	}
	debugf("deleting: key=%s", key)
	luaFn := func(con redisConn) error {
		_, err := callLua(c.rdb.Context(), con, ` --  delete
		local v = redis.call('HGET', KEYS[1], 'value')
		if v == false then
			return
		end
		redis.call('HSET', KEYS[1], 'lockUtil', 0)
		redis.call('HDEL', KEYS[1], 'lockOwner')
		redis.call('EXPIRE', KEYS[1], ARGV[1])
			`, []string{key}, []interface{}{c.Options.Delay})
		return err
	}
	if c.Options.WaitReplicas > 0 {
		rconn := c.rdb.Conn(c.rdb.Context())
		defer func() {
			_ = rconn.Close()
		}()
		err := luaFn(rconn)
		cmd := redis.NewCmd(c.rdb.Context(), "WAIT", c.Options.WaitReplicas, c.Options.WaitReplicasTimeout)
		if err == nil && c.Options.WaitReplicas > 0 {
			err = rconn.Process(c.rdb.Context(), cmd)
		}
		var replicas int
		if err == nil {
			replicas, err = cmd.Int()
		}
		if err == nil && replicas < c.Options.WaitReplicas {
			err = fmt.Errorf("wait replicas %d failed. result replicas: %d", c.Options.WaitReplicas, replicas)
		}
		return err
	}
	return luaFn(c.rdb)
}

// Fetch returns the value store in cache indexed by the key.
// If the key doest not exists, call fn to get result, store it in cache, then return.
func (c *Client) Fetch(key string, expire time.Duration, fn func() (string, error)) (string, error) {
	ex := expire - c.Options.Delay - time.Duration(rand.Float64()*c.Options.RandomExpireAdjustment*float64(expire))
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

func (c *Client) luaGet(key string, owner string) ([]interface{}, error) {
	res, err := callLua(c.rdb.Context(), c.rdb, ` -- luaGet
	local v = redis.call('HGET', KEYS[1], 'value')
	local lu = redis.call('HGET', KEYS[1], 'lockUtil')
	if lu ~= false and tonumber(lu) < tonumber(ARGV[1]) or lu == false and v == false then
		redis.call('HSET', KEYS[1], 'lockUtil', ARGV[2])
		redis.call('HSET', KEYS[1], 'lockOwner', ARGV[3])
		return { v, 'LOCKED' }
	end
	return {v, lu}
	`, []string{key}, []interface{}{now(), now() + int64(c.Options.LockExpire), owner})
	debugf("luaGet return: %v, %v", res, err)
	if err != nil {
		return nil, err
	}
	return res.([]interface{}), nil
}

func (c *Client) luaSet(key string, value string, expire int, owner string) error {
	_, err := callLua(c.rdb.Context(), c.rdb, `-- luaSet
	local o = redis.call('HGET', KEYS[1], 'lockOwner')
	if o ~= ARGV[2] then
			return
	end
	redis.call('HSET', KEYS[1], 'value', ARGV[1])
	redis.call('HDEL', KEYS[1], 'lockUtil')
	redis.call('HDEL', KEYS[1], 'lockOwner')
	redis.call('EXPIRE', KEYS[1], ARGV[3])
	`, []string{key}, []interface{}{value, owner, expire})
	return err
}

func (c *Client) fetchNew(key string, expire time.Duration, owner string, fn func() (string, error)) (string, error) {
	result, err := fn()
	if err != nil {
		return "", err
	}
	if result == "" {
		if c.Options.EmptyExpire == 0 { // if empty expire is 0, then delete the key
			err = c.rdb.Del(c.rdb.Context(), key).Err()
			return "", err
		}
		expire = c.Options.EmptyExpire
	}
	err = c.luaSet(key, result, int(expire/time.Second), owner)
	return result, err
}

func (c *Client) weakFetch(key string, expire time.Duration, fn func() (string, error)) (string, error) {
	debugf("weakFetch: key=%s", key)
	owner := shortuuid.New()
	r, err := c.luaGet(key, owner)
	for err == nil && r[0] == nil && r[1].(string) != locked {
		debugf("empty result for %s locked by other, so sleep %d ms", key, c.Options.LockSleep)
		time.Sleep(c.Options.LockSleep)
		r, err = c.luaGet(key, owner)
	}
	if err != nil {
		return "", err
	}
	if r[1] != locked {
		return r[0].(string), nil
	}
	if r[0] == nil {
		return c.fetchNew(key, expire, owner, fn)
	}
	go withRecover(func() {
		_, _ = c.fetchNew(key, expire, owner, fn)
	})
	return r[0].(string), nil
}

func (c *Client) strongFetch(key string, expire time.Duration, fn func() (string, error)) (string, error) {
	debugf("strongFetch: key=%s", key)
	owner := shortuuid.New()
	r, err := c.luaGet(key, owner)
	for err == nil && r[1] != nil && r[1] != locked { // locked by other
		debugf("locked by other, so sleep %d ms", c.Options.LockSleep)
		time.Sleep(c.Options.LockSleep)
		r, err = c.luaGet(key, owner)
	}
	if err != nil {
		return "", err
	}
	if r[1] != locked { // normal value
		return r[0].(string), nil
	}
	return c.fetchNew(key, expire, owner, fn)
}

// RawGet returns the value store in cache indexed by the key, no matter if the key locked or not
func (c *Client) RawGet(key string) (string, error) {
	return c.rdb.HGet(c.rdb.Context(), key, "value").Result()
}

// RawSet sets the value store in cache indexed by the key, no matter if the key locked or not
func (c *Client) RawSet(key string, value string, expire time.Duration) error {
	err := c.rdb.HSet(c.rdb.Context(), key, "value", value).Err()
	if err == nil {
		err = c.rdb.Expire(c.rdb.Context(), key, expire).Err()
	}
	return err
}

// LockForUpdate locks the key, used in very strict strong consistency mode
func (c *Client) LockForUpdate(key string, owner string) error {
	lockUtil := math.Pow10(10)
	res, err := callLua(c.rdb.Context(), c.rdb, ` -- luaLock
	local lu = redis.call('HGET', KEYS[1], 'lockUtil')
	local lo = redis.call('HGET', KEYS[1], 'lockOwner')
	if lu == false or tonumber(lu) < tonumber(ARGV[2]) or lo == ARGV[1] then
		redis.call('HSET', KEYS[1], 'lockUtil', ARGV[2])
		redis.call('HSET', KEYS[1], 'lockOwner', ARGV[1])
		return 'LOCKED'
	end
	return lo
	`, []string{key}, []interface{}{owner, lockUtil})
	if err == nil && res != "LOCKED" {
		return fmt.Errorf("%s has been locked by %s", key, res)
	}
	return err
}

// UnlockForUpdate unlocks the key, used in very strict strong consistency mode
func (c *Client) UnlockForUpdate(key string, owner string) error {
	_, err := callLua(c.rdb.Context(), c.rdb, ` -- luaUnlock
	local lo = redis.call('HGET', KEYS[1], 'lockOwner')
	if lo == ARGV[1] then
		redis.call('DEL', KEYS[1])
	end
	`, []string{key}, []interface{}{owner})
	return err
}
