package rockscache

import (
	"errors"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func genBatchDataFunc(values map[int]string, sleepMilli int) func(idxs []int) (map[int]string, error) {
	return func(idxs []int) (map[int]string, error) {
		time.Sleep(time.Duration(sleepMilli) * time.Millisecond)
		return values, nil
	}
}

func genIdxs(from, to int) (idxs []int) {
	for i := from; i < to; i++ {
		idxs = append(idxs, i)
	}
	return
}

func genKeys(idxs []int) (keys []string) {
	for _, i := range idxs {
		suffix := strconv.Itoa(i)
		k := "key" + suffix
		keys = append(keys, k)
	}
	return
}

func genValues(n int, prefix string) map[int]string {
	values := make(map[int]string)
	for i := 0; i < n; i++ {
		v := prefix + strconv.Itoa(i)
		values[i] = v
	}
	return values
}

func TestStrongFetchBatch(t *testing.T) {
	clearCache()
	rc := NewClient(rdb, NewDefaultOptions())
	rc.Options.StrongConsistency = true
	began := time.Now()
	n := int(rand.Int31n(20) + 10)
	idxs := genIdxs(0, n)
	keys, values := genKeys(idxs), genValues(n, "value_")
	go func() {
		dc2 := NewClient(rdb, NewDefaultOptions())
		v, err := dc2.FetchBatch(keys, 60*time.Second, genBatchDataFunc(values, 200))
		assert.Nil(t, err)
		assert.Equal(t, values, v)
	}()
	time.Sleep(20 * time.Millisecond)

	v, err := rc.FetchBatch(keys, 60*time.Second, genBatchDataFunc(values, 200))
	assert.Nil(t, err)
	assert.Equal(t, values, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	for _, k := range keys {
		err = rc.TagAsDeleted(k)
		assert.Nil(t, err)
	}

	began = time.Now()
	nv := genValues(n, "value_")
	v, err = rc.FetchBatch(keys, 60*time.Second, genBatchDataFunc(nv, 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	ignored := genValues(n, "value_")
	v, err = rc.FetchBatch(keys, 60*time.Second, genBatchDataFunc(ignored, 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)
}

func TestStrongFetchBatchOverlap(t *testing.T) {
	clearCache()
	rc := NewClient(rdb, NewDefaultOptions())
	rc.Options.StrongConsistency = true
	began := time.Now()
	n := 100
	idxs := genIdxs(0, n)
	keys := genKeys(idxs)
	keys1, values1 := keys[:60], genValues(60, "value_")
	keys2, values2 := keys[40:], genValues(60, "eulav_")

	go func() {
		dc2 := NewClient(rdb, NewDefaultOptions())
		v, err := dc2.FetchBatch(keys1, 20*time.Second, genBatchDataFunc(values1, 200))
		assert.Nil(t, err)
		assert.Equal(t, values1, v)
	}()
	time.Sleep(20 * time.Millisecond)

	v, err := rc.FetchBatch(keys2, 20*time.Second, genBatchDataFunc(values2, 200))
	assert.Nil(t, err)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)
	for i := 40; i < 60; i++ {
		assert.Equal(t, keys2[i-40], keys1[i])
		assert.Equal(t, values1[i], v[i-40])
	}
	for i := 60; i < n; i++ {
		assert.Equal(t, values2[i-40], v[i-40])
	}
}

func TestStrongFetchBatchOverlapExpire(t *testing.T) {
	clearCache()
	opts := NewDefaultOptions()
	opts.Delay = 10 * time.Millisecond
	opts.StrongConsistency = true

	rc := NewClient(rdb, opts)
	began := time.Now()
	n := 100
	idxs := genIdxs(0, n)
	keys := genKeys(idxs)
	keys1, values1 := keys[:60], genValues(60, "value_")
	keys2, values2 := keys[40:], genValues(60, "eulav_")

	v, err := rc.FetchBatch(keys1, 2*time.Second, genBatchDataFunc(values1, 200))
	assert.Nil(t, err)
	assert.Equal(t, values1, v)

	v, err = rc.FetchBatch(keys2, 2*time.Second, genBatchDataFunc(values2, 200))
	assert.Nil(t, err)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)
	for i := 40; i < 60; i++ {
		assert.Equal(t, keys2[i-40], keys1[i])
		assert.Equal(t, values1[i], v[i-40])
	}
	for i := 60; i < n; i++ {
		assert.Equal(t, values2[i-40], v[i-40])
	}

	time.Sleep(time.Second)
	v, err = rc.FetchBatch(keys2, 2*time.Second, genBatchDataFunc(values2, 200))
	assert.Nil(t, err)
	assert.Nil(t, err)
	assert.Equal(t, values2, v)
}

func TestStrongErrorFetchBatch(t *testing.T) {
	rc := NewClient(rdb, NewDefaultOptions())
	rc.Options.StrongConsistency = true

	clearCache()
	began := time.Now()

	n := 100
	idxs := genIdxs(0, n)
	keys := genKeys(idxs)

	fetchError := errors.New("fetch error")
	getFn := func(idxs []int) (map[int]string, error) {
		return nil, fetchError
	}
	_, err := rc.FetchBatch(keys, 60*time.Second, getFn)
	assert.Error(t, err)
	fetchError = nil
	_, err = rc.FetchBatch(keys, 60*time.Second, getFn)
	assert.Nil(t, err)
	assert.True(t, time.Since(began) < time.Duration(150)*time.Millisecond)
}
