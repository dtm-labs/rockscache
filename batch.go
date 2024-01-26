package rockscache

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime/debug"
	"sync"
	"time"

	"github.com/lithammer/shortuuid"
	"github.com/redis/go-redis/v9"
)

var (
	errNeedFetch      = errors.New("need fetch")
	errNeedAsyncFetch = errors.New("need async fetch")
)

func (c *Client) luaGetBatch(ctx context.Context, keys []string, owner string) ([]interface{}, error) {
	res, err := callLua(ctx, c.rdb, getBatchScript, keys, []interface{}{now(), now() + int64(c.Options.LockExpire/time.Second), owner})
	debugf("luaGetBatch return: %v, %v", res, err)
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
	_, err := callLua(ctx, c.rdb, setBatchScript, keys, vals)
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

type pair struct {
	idx  int
	data string
	err  error
}

func (c *Client) weakFetchBatch(ctx context.Context, keys []string, expire time.Duration, fn func(idxs []int) (map[int]string, error)) (map[int]string, error) {
	debugf("batch: weakFetch keys=%+v", keys)
	var result = make(map[int]string)
	owner := shortuuid.New()
	var toGet, toFetch, toFetchAsync []int

	// read from redis without sleep
	rs, err := c.luaGetBatch(ctx, keys, owner)
	if err != nil {
		return nil, err
	}
	for i, v := range rs {
		r := v.([]interface{})

		if r[0] == nil {
			if r[1] == locked {
				toFetch = append(toFetch, i)
			} else {
				toGet = append(toGet, i)
			}
			continue
		}

		if r[1] == locked {
			toFetchAsync = append(toFetchAsync, i)
			// fallthrough with old data
		} // else new data

		result[i] = r[0].(string)
	}

	if len(toFetchAsync) > 0 {
		go func(idxs []int) {
			debugf("batch weak: async fetch keys=%+v", keys)
			_, _ = c.fetchBatch(ctx, keys, idxs, expire, owner, fn)
		}(toFetchAsync)
		toFetchAsync = toFetchAsync[:0] // reset toFetch
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
		toFetch = toFetch[:0] // reset toFetch
	}

	if len(toGet) > 0 {
		// read from redis and sleep to wait
		var wg sync.WaitGroup

		var ch = make(chan pair, len(toGet))
		for _, idx := range toGet {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r, err := c.luaGet(ctx, keys[i], owner)
				ticker := time.NewTimer(c.Options.LockSleep)
				defer ticker.Stop()
				for err == nil && r[0] == nil && r[1].(string) != locked {
					debugf("batch weak: empty result for %s locked by other, so sleep %s", keys[i], c.Options.LockSleep.String())
					select {
					case <-ctx.Done():
						ch <- pair{idx: i, err: ctx.Err()}
						return
					case <-ticker.C:
						// equal to time.Sleep(c.Options.LockSleep) but can be canceled
					}
					r, err = c.luaGet(ctx, keys[i], owner)
					// Reset ticker after luaGet
					// If we reset ticker before luaGet, since luaGet takes a period of time,
					// the actual sleep time will be shorter than expected
					if !ticker.Stop() && len(ticker.C) > 0 {
						<-ticker.C
					}
					ticker.Reset(c.Options.LockSleep)
				}
				if err != nil {
					ch <- pair{idx: i, data: "", err: err}
					return
				}
				if r[1] != locked { // normal value
					ch <- pair{idx: i, data: r[0].(string), err: nil}
					return
				}
				if r[0] == nil {
					ch <- pair{idx: i, data: "", err: errNeedFetch}
					return
				}
				ch <- pair{idx: i, data: "", err: errNeedAsyncFetch}
			}(idx)
		}
		wg.Wait()
		close(ch)

		for p := range ch {
			if p.err != nil {
				switch p.err {
				case errNeedFetch:
					toFetch = append(toFetch, p.idx)
					continue
				case errNeedAsyncFetch:
					toFetchAsync = append(toFetchAsync, p.idx)
					continue
				default:
				}
				return nil, p.err
			}
			result[p.idx] = p.data
		}
	}

	if len(toFetchAsync) > 0 {
		go func(idxs []int) {
			debugf("batch weak: async 2 fetch keys=%+v", keys)
			_, _ = c.fetchBatch(ctx, keys, idxs, expire, owner, fn)
		}(toFetchAsync)
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
		for _, k := range toFetch {
			result[k] = fetched[k]
		}
		toFetch = toFetch[:0] // reset toFetch
	}

	if len(toGet) > 0 {
		// read from redis and sleep to wait
		var wg sync.WaitGroup
		var ch = make(chan pair, len(toGet))
		for _, idx := range toGet {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r, err := c.luaGet(ctx, keys[i], owner)
				ticker := time.NewTimer(c.Options.LockSleep)
				defer ticker.Stop()
				for err == nil && r[1] != nil && r[1] != locked { // locked by other
					debugf("batch: locked by other, so sleep %s", c.Options.LockSleep)
					select {
					case <-ctx.Done():
						ch <- pair{idx: i, err: ctx.Err()}
						return
					case <-ticker.C:
						// equal to time.Sleep(c.Options.LockSleep) but can be canceled
					}
					r, err = c.luaGet(ctx, keys[i], owner)
					// Reset ticker after luaGet
					// If we reset ticker before luaGet, since luaGet takes a period of time,
					// the actual sleep time will be shorter than expected
					if !ticker.Stop() && len(ticker.C) > 0 {
						<-ticker.C
					}
					ticker.Reset(c.Options.LockSleep)
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

// FetchBatch returns a map with values indexed by index of keys list.
// 1. the first parameter is the keys list of the data
// 2. the second parameter is the data expiration time
// 3. the third parameter is the batch data fetch function which is called when the cache does not exist
// the parameter of the batch data fetch function is the index list of those keys
// missing in cache, which can be used to form a batch query for missing data.
// the return value of the batch data fetch function is a map, with key of the
// index and value of the corresponding data in form of string
func (c *Client) FetchBatch(keys []string, expire time.Duration, fn func(idxs []int) (map[int]string, error)) (map[int]string, error) {
	return c.FetchBatch2(c.Options.Context, keys, expire, fn)
}

// FetchBatch2 is same with FetchBatch, except that a user defined context.Context can be provided.
func (c *Client) FetchBatch2(ctx context.Context, keys []string, expire time.Duration, fn func(idxs []int) (map[int]string, error)) (map[int]string, error) {
	if c.Options.DisableCacheRead {
		return fn(c.keysIdx(keys))
	} else if c.Options.StrongConsistency {
		return c.strongFetchBatch(ctx, keys, expire, fn)
	}
	return c.weakFetchBatch(ctx, keys, expire, fn)
}

// TagAsDeletedBatch a key list, the keys in list will expire after delay time.
func (c *Client) TagAsDeletedBatch(keys []string) error {
	return c.TagAsDeletedBatch2(c.Options.Context, keys)
}

// TagAsDeletedBatch2 a key list, the keys in list will expire after delay time.
func (c *Client) TagAsDeletedBatch2(ctx context.Context, keys []string) error {
	if c.Options.DisableCacheDelete {
		return nil
	}
	debugf("batch deleting: keys=%v", keys)
	luaFn := func(con redis.Scripter) error {
		_, err := callLua(ctx, con, deleteBatchScript, keys, []interface{}{int64(c.Options.Delay / time.Second)})
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
