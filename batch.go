package rockscache

import (
	"context"
	"errors"
	"math/rand"
	"runtime/debug"
	"sync"
	"time"

	"github.com/lithammer/shortuuid"
)

var (
	errNeedFetch = errors.New("need fetch")
)

func (c *Client) luaGetBatch(ctx context.Context, keys []string, owner string) ([]interface{}, error) {
	res, err := callLua(ctx, c.rdb, ` -- luaGetBatch
    local rets = {}
    for i, key in ipairs(KEYS)
    do
        local v = redis.call('HGET', key, 'value')
        local lu = redis.call('HGET', key, 'lockUtil')
        if lu ~= false and tonumber(lu) < tonumber(ARGV[1]) or lu == false and v == false then
            redis.call('HSET', key, 'lockUtil', ARGV[2])
            redis.call('HSET', key, 'lockOwner', ARGV[3])
            table.insert(rets, { v, 'LOCKED' })
        else
            table.insert(rets, {v, lu})
        end
    end
    return rets
	`, keys, []interface{}{now(), now() + int64(c.Options.LockExpire/time.Second), owner})
	debugf("luaGet return: %v, %v", res, err)
	if err != nil {
		return nil, err
	}
	return res.([]interface{}), nil
}

func (c *Client) luaSetBatch(ctx context.Context, keys []string, values []string, expires []int, owner string) error {
	var vals = make([]interface{}, 0, 2+len(values))
	vals = append(vals, owner)
	for _, v := range values {
		vals = append(vals, v)
	}
	for _, ex := range expires {
		vals = append(vals, ex)
	}
	_, err := callLua(ctx, c.rdb, `-- luaSetBatch
    local n = #KEYS
    for i, key in ipairs(KEYS)
    do
        local o = redis.call('HGET', key, 'lockOwner')
        if o ~= ARGV[1] then
                return
        end
        redis.call('HSET', key, 'value', ARGV[i+1])
        redis.call('HDEL', key, 'lockUtil')
        redis.call('HDEL', key, 'lockOwner')
        redis.call('EXPIRE', key, ARGV[i+1+n])
    end
	`, keys, vals)
	return err
}

func (c *Client) fetchBatch(ctx context.Context, keys []string, idxs []int, expire time.Duration, owner string, fn func(idxs []int) (map[int]string, error)) (map[int]string, error) {
	defer func() {
		if r := recover(); r != nil {
			debug.PrintStack()
		}
	}()
	data, err := fn(idxs)
	if err != nil {
		for _, idx := range idxs {
			_ = c.UnlockForUpdate(ctx, keys[idx], owner)
		}
		return nil, err
	}

	if data == nil {
		// incase data is nil
		data = make(map[int]string)
	}

	var batchKeys []string
	var batchValues []string
	var batchExpires []int

	for _, idx := range idxs {
		v := data[idx]
		ex := expire - c.Options.Delay - time.Duration(rand.Float64()*c.Options.RandomExpireAdjustment*float64(expire))
		if v == "" {
			if c.Options.EmptyExpire == 0 { // if empty expire is 0, then delete the key
				_ = c.rdb.Del(ctx, keys[idx]).Err()
				if err != nil {
					debugf("batch: del failed key=%s err:%s", keys[idx], err.Error())
				}
				continue
			}
			ex = c.Options.EmptyExpire

			data[idx] = v // incase idx not in data
		}
		batchKeys = append(batchKeys, keys[idx])
		batchValues = append(batchValues, v)
		batchExpires = append(batchExpires, int(ex/time.Second))
	}

	err = c.luaSetBatch(ctx, batchKeys, batchValues, batchExpires, owner)
	if err != nil {
		debugf("batch: luaSetBatch failed keys=%s err:%s", keys, err.Error())
	}
	return data, nil
}

func (c *Client) keysIdx(keys []string) (idxs []int) {
	for i := range keys {
		idxs = append(idxs, i)
	}
	return idxs
}

func (c *Client) strongFetchBatch(ctx context.Context, keys []string, expire time.Duration, fn func(idxs []int) (map[int]string, error)) (map[int]string, error) {
	debugf("batch: strongFetch keys=%+v", keys)
	var result = make(map[int]string)
	owner := shortuuid.New()
	var toGet, toFetch []int

	// read from redis without sleep
	rs, err := c.luaGetBatch(ctx, keys, owner)
	if err != nil {
		return nil, err
	}
	for i, v := range rs {
		r := v.([]interface{})
		if r[1] == nil { // normal value
			result[i] = r[0].(string)
			continue
		}

		if r[1] != locked { // locked by other
			debugf("batch: locked by other, continue idx=%d", i)
			toGet = append(toGet, i)
			continue
		}

		// locked for fetch
		toFetch = append(toFetch, i)
	}

	if len(toFetch) > 0 {
		// batch fetch
		fetched, err := c.fetchBatch(ctx, keys, toFetch, expire, owner, fn)
		if err != nil {
			return nil, err
		}
		for k, v := range fetched {
			result[k] = v
		}
		toFetch = toFetch[:0] // reset toFetch
	}

	if len(toGet) > 0 {
		// read from redis and sleep to wait
		var wg sync.WaitGroup
		type pair struct {
			idx  int
			data string
			err  error
		}
		var ch = make(chan pair, len(toGet))
		for _, idx := range toGet {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r, err := c.luaGet(ctx, keys[i], owner)
				for err == nil && r[1] != nil && r[1] != locked { // locked by other
					debugf("batch: locked by other, so sleep %s", c.Options.LockSleep)
					time.Sleep(c.Options.LockSleep)
					r, err = c.luaGet(ctx, keys[i], owner)
				}
				if err != nil {
					ch <- pair{idx: i, data: "", err: err}
					return
				}
				if r[1] != locked { // normal value
					ch <- pair{idx: i, data: r[0].(string), err: nil}
					return
				}
				// locked for update
				ch <- pair{idx: i, data: "", err: errNeedFetch}
			}(idx)
		}
		wg.Wait()
		close(ch)
		for p := range ch {
			if p.err != nil {
				if p.err == errNeedFetch {
					toFetch = append(toFetch, p.idx)
					continue
				}
				return nil, p.err
			}
			result[p.idx] = p.data
		}
	}

	if len(toFetch) > 0 {
		// batch fetch
		fetched, err := c.fetchBatch(ctx, keys, toFetch, expire, owner, fn)
		if err != nil {
			return nil, err
		}
		for _, k := range toFetch {
			result[k] = fetched[k]
		}
	}

	return result, nil
}

func (c *Client) FetchBatch(keys []string, expire time.Duration, fn func(idxs []int) (map[int]string, error)) (map[int]string, error) {
	return c.FetchBatch2(c.rdb.Context(), keys, expire, fn)
}

func (c *Client) FetchBatch2(ctx context.Context, keys []string, expire time.Duration, fn func(idxs []int) (map[int]string, error)) (map[int]string, error) {
	if c.Options.DisableCacheRead {
		return fn(c.keysIdx(keys))
	}
	// there's no weak fetch batch
	return c.strongFetchBatch(ctx, keys, expire, fn)
}
