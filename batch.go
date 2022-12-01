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
		err = c.luaSet(ctx, keys[idx], v, int(ex/time.Second), owner)
		if err != nil {
			debugf("batch: luaSet failed key=%s err:%s", keys[idx], err.Error())
		}
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
	var idxs = c.keysIdx(keys)
	var toGet, toFetch []int

	// read from redis without sleep
	for _, idx := range idxs {
		r, err := c.luaGet(ctx, keys[idx], owner)
		if err != nil {
			return nil, err
		}

		if r[1] == nil { // normal value
			result[idx] = r[0].(string)
			continue
		}

		if r[1] != locked { // locked by other
			debugf("batch: locked by other, continue idx=%d", idx)
			toGet = append(toGet, idx)
			continue
		}

		// locked for fetch
		toFetch = append(toFetch, idx)
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
		for k, v := range fetched {
			result[k] = v
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
