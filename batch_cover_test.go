package rockscache

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var (
	n = int(rand.Int31n(20) + 10)
)

func TestDisableForBatch(t *testing.T) {
	idxs := genIdxs(0, n)
	keys := genKeys(idxs)
	getFn := func(idxs []int) (map[int]string, error) {
		return nil, nil
	}

	rc := NewClient(nil, NewDefaultOptions())
	rc.Options.DisableCacheDelete = true
	rc.Options.DisableCacheRead = true

	_, err := rc.FetchBatch2(context.Background(), keys, 60, getFn)
	assert.Nil(t, err)
	err = rc.TagAsDeleted2(context.Background(), keys[0])
	assert.Nil(t, err)
}

func TestErrorFetchForBatch(t *testing.T) {
	idxs := genIdxs(0, n)
	keys := genKeys(idxs)
	fetchError := errors.New("fetch error")
	fn := func(idxs []int) (map[int]string, error) {
		return nil, fetchError
	}
	clearCache()
	rc := NewClient(rdb, NewDefaultOptions())
	_, err := rc.FetchBatch(keys, 60, fn)
	assert.ErrorIs(t, err, fetchError)

	rc.Options.StrongConsistency = true
	_, err = rc.FetchBatch(keys, 60, fn)
	assert.ErrorIs(t, err, fetchError)
}

func TestEmptyExpireForBatch(t *testing.T) {
	testEmptyExpireForBatch(t, 0)
	testEmptyExpireForBatch(t, 10*time.Second)
}

func testEmptyExpireForBatch(t *testing.T, expire time.Duration) {
	idxs := genIdxs(0, n)
	keys := genKeys(idxs)
	fn := func(idxs []int) (map[int]string, error) {
		return nil, nil
	}
	fetchError := errors.New("fetch error")
	errFn := func(idxs []int) (map[int]string, error) {
		return nil, fetchError
	}

	clearCache()
	rc := NewClient(rdb, NewDefaultOptions())
	rc.Options.EmptyExpire = expire

	_, err := rc.FetchBatch(keys, 60, fn)
	assert.Nil(t, err)
	_, err = rc.FetchBatch(keys, 60, errFn)
	if expire == 0 {
		assert.ErrorIs(t, err, fetchError)
	} else {
		assert.Nil(t, err)
	}

	clearCache()
	rc.Options.StrongConsistency = true
	_, err = rc.FetchBatch(keys, 60, fn)
	assert.Nil(t, err)
	_, err = rc.FetchBatch(keys, 60, errFn)
	if expire == 0 {
		assert.ErrorIs(t, err, fetchError)
	} else {
		assert.Nil(t, err)
	}
}

func TestPanicFetchForBatch(t *testing.T) {
	idxs := genIdxs(0, n)
	keys := genKeys(idxs)
	fn := func(idxs []int) (map[int]string, error) {
		return nil, nil
	}
	fetchError := errors.New("fetch error")
	errFn := func(idxs []int) (map[int]string, error) {
		panic(fetchError)
	}
	clearCache()
	rc := NewClient(rdb, NewDefaultOptions())

	_, err := rc.FetchBatch(keys, 60, fn)
	assert.Nil(t, err)
	rc.TagAsDeleted("key1")
	_, err = rc.FetchBatch(keys, 60, errFn)
	assert.Nil(t, err)
	time.Sleep(20 * time.Millisecond)
}

// func TestTagAsDeletedWait(t *testing.T) {
// 	clearCache()
// 	rc := NewClient(rdb, NewDefaultOptions())
// 	rc.Options.WaitReplicas = 1
// 	rc.Options.WaitReplicasTimeout = 10
// 	err := rc.TagAsDeleted("key1")
// 	if getCluster() != nil {
// 		assert.Nil(t, err)
// 	} else {
// 		assert.Error(t, err, fmt.Errorf("wait replicas 1 failed. result replicas: 0"))
// 	}
// }
