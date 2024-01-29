package rockscache

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/lithammer/shortuuid"
	"github.com/redis/go-redis/v9"
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
	// if WaitReplicas is > 0, it will use redis WAIT command to wait for TagAsDeleted synchronized.
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
	// Context for redis command
	Context context.Context
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
		Context:                context.Background(),
	}
}

// Client delay client
type Client struct {
	rdb     redis.UniversalClient
	Options Options
	group   singleflight.Group
}

// NewClient return a new rockscache client
// for each key, rockscache client store a hash set,
// the hash set contains the following fields:
// value: the value of the key
// lockUntil: the time when the lock is released.
// lockOwner: the owner of the lock.
// if a thread query the cache for data, and no cache exists, it will lock the key before querying data in DB
func NewClient(rdb redis.UniversalClient, options Options) *Client {
	if options.Delay == 0 || options.LockExpire == 0 {
		panic("cache options error: Delay and LockExpire should not be 0, you should call NewDefaultOptions() to get default options")
	}
	return &Client{rdb: rdb, Options: options}
}

// TagAsDeleted a key, the key will expire after delay time.
func (c *Client) TagAsDeleted(key string) error {
	return c.TagAsDeleted2(c.Options.Context, key)
}

// TagAsDeleted2 a key, the key will expire after delay time.
func (c *Client) TagAsDeleted2(ctx context.Context, key string) error {
	if c.Options.DisableCacheDelete {
		return nil
	}
	debugf("deleting: key=%s", key)
	luaFn := func(con redis.Scripter) error {
		_, err := callLua(ctx, con, deleteScript, []string{key}, []interface{}{int64(c.Options.Delay / time.Second)})
		return err
	}
	if c.Options.WaitReplicas > 0 {
		err := luaFn(c.rdb)
		cmd := redis.NewCmd(ctx, "WAIT", c.Options.WaitReplicas, c.Options.WaitReplicasTimeout)
		if err == nil {
			err = c.rdb.Process(ctx, cmd)
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
	return c.Fetch2(c.Options.Context, key, expire, fn)
}

// Fetch2 returns the value store in cache indexed by the key.
// If the key doest not exists, call fn to get result, store it in cache, then return.
func (c *Client) Fetch2(ctx context.Context, key string, expire time.Duration, fn func() (string, error)) (string, error) {
	ex := expire - c.Options.Delay - time.Duration(rand.Float64()*c.Options.RandomExpireAdjustment*float64(expire))
	v, err, _ := c.group.Do(key, func() (interface{}, error) {
		if c.Options.DisableCacheRead {
			return fn()
		} else if c.Options.StrongConsistency {
			return c.strongFetch(ctx, key, ex, fn)
		}
		return c.weakFetch(ctx, key, ex, fn)
	})
	return v.(string), err
}

func (c *Client) luaGet(ctx context.Context, key string, owner string) ([]interface{}, error) {
	res, err := callLua(ctx, c.rdb, getScript, []string{key}, []interface{}{now(), now() + int64(c.Options.LockExpire/time.Second), owner})
	debugf("luaGet return: %v, %v", res, err)
	if err != nil {
		return nil, err
	}
	return res.([]interface{}), nil
}

func (c *Client) luaSet(ctx context.Context, key string, value string, expire int, owner string) error {
	_, err := callLua(ctx, c.rdb, setScript, []string{key}, []interface{}{value, owner, expire})
	return err
}

func (c *Client) fetchNew(ctx context.Context, key string, expire time.Duration, owner string, fn func() (string, error)) (string, error) {
	result, err := fn()
	if err != nil {
		_ = c.UnlockForUpdate(ctx, key, owner)
		return "", err
	}
	if result == "" {
		if c.Options.EmptyExpire == 0 { // if empty expire is 0, then delete the key
			err = c.rdb.Del(ctx, key).Err()
			return "", err
		}
		expire = c.Options.EmptyExpire
	}
	err = c.luaSet(ctx, key, result, int(expire/time.Second), owner)
	return result, err
}

func (c *Client) weakFetch(ctx context.Context, key string, expire time.Duration, fn func() (string, error)) (string, error) {
	debugf("weakFetch: key=%s", key)
	owner := shortuuid.New()
	r, err := c.luaGet(ctx, key, owner)
	for err == nil && r[0] == nil && r[1].(string) != locked {
		debugf("empty result for %s locked by other, so sleep %s", key, c.Options.LockSleep.String())
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(c.Options.LockSleep):
			// equal to time.Sleep(c.Options.LockSleep) but can be canceled
		}
		r, err = c.luaGet(ctx, key, owner)
	}
	if err != nil {
		return "", err
	}
	if r[1] != locked {
		return r[0].(string), nil
	}
	if r[0] == nil {
		return c.fetchNew(ctx, key, expire, owner, fn)
	}
	go withRecover(func() {
		_, _ = c.fetchNew(ctx, key, expire, owner, fn)
	})
	return r[0].(string), nil
}

func (c *Client) strongFetch(ctx context.Context, key string, expire time.Duration, fn func() (string, error)) (string, error) {
	debugf("strongFetch: key=%s", key)
	owner := shortuuid.New()
	r, err := c.luaGet(ctx, key, owner)
	for err == nil && r[1] != nil && r[1] != locked { // locked by other
		debugf("locked by other, so sleep %s", c.Options.LockSleep)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(c.Options.LockSleep):
			// equal to time.Sleep(c.Options.LockSleep) but can be canceled
		}
		r, err = c.luaGet(ctx, key, owner)
	}
	if err != nil {
		return "", err
	}
	if r[1] != locked { // normal value
		return r[0].(string), nil
	}
	return c.fetchNew(ctx, key, expire, owner, fn)
}

// RawGet returns the value store in cache indexed by the key, no matter if the key locked or not
func (c *Client) RawGet(ctx context.Context, key string) (string, error) {
	return c.rdb.HGet(ctx, key, "value").Result()
}

// RawSet sets the value store in cache indexed by the key, no matter if the key locked or not
func (c *Client) RawSet(ctx context.Context, key string, value string, expire time.Duration) error {
	err := c.rdb.HSet(ctx, key, "value", value).Err()
	if err == nil {
		err = c.rdb.Expire(ctx, key, expire).Err()
	}
	return err
}

// LockForUpdate locks the key, used in very strict strong consistency mode
func (c *Client) LockForUpdate(ctx context.Context, key string, owner string) error {
	lockUntil := math.Pow10(10)
	res, err := callLua(ctx, c.rdb, lockScript, []string{key}, []interface{}{owner, lockUntil})
	if err == nil && res != "LOCKED" {
		return fmt.Errorf("%s has been locked by %s", key, res)
	}
	return err
}

// UnlockForUpdate unlocks the key, used in very strict strong consistency mode
func (c *Client) UnlockForUpdate(ctx context.Context, key string, owner string) error {
	_, err := callLua(ctx, c.rdb, unlockScript, []string{key}, []interface{}{owner, c.Options.LockExpire / time.Second})
	return err
}
