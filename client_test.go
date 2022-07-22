package rockscache

import (
	"errors"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
)

var rdbKey = "client-test-key"

var rdb = redis.NewClient(&redis.Options{
	Addr:     "localhost:6379",
	Username: "root",
	Password: "",
})

func clearCache() {
	err := rdb.FlushAll(rdb.Context()).Err()
	if err != nil {
		panic(err)
	}
}

func genDataFunc(value string, sleepMilli int) func() (string, error) {
	return func() (string, error) {
		time.Sleep(time.Duration(sleepMilli) * time.Millisecond)
		return value, nil
	}
}

func init() {
	SetVerbose(true)
}
func TestWeakFetch(t *testing.T) {
	rc := NewClient(rdb, NewDefaultOptions())

	clearCache()
	began := time.Now()
	expected := "value1"
	go func() {
		dc2 := NewClient(rdb, NewDefaultOptions())
		v, err := dc2.Fetch(rdbKey, 60*time.Second, genDataFunc(expected, 200))
		assert.Nil(t, err)
		assert.Equal(t, expected, v)
	}()
	time.Sleep(20 * time.Millisecond)

	v, err := rc.Fetch(rdbKey, 60*time.Second, genDataFunc(expected, 201))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	err = rc.TagAsDeleted(rdbKey)
	assert.Nil(t, err)

	nv := "value2"
	v, err = rc.Fetch(rdbKey, 60*time.Second, genDataFunc(nv, 200))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)

	time.Sleep(300 * time.Millisecond)
	v, err = rc.Fetch(rdbKey, 60*time.Second, genDataFunc("ignored", 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)
}

func TestStrongFetch(t *testing.T) {
	clearCache()
	rc := NewClient(rdb, NewDefaultOptions())
	rc.Options.StrongConsistency = true
	began := time.Now()
	expected := "value1"
	go func() {
		dc2 := NewClient(rdb, NewDefaultOptions())
		v, err := dc2.Fetch(rdbKey, 60*time.Second, genDataFunc(expected, 200))
		assert.Nil(t, err)
		assert.Equal(t, expected, v)
	}()
	time.Sleep(20 * time.Millisecond)

	v, err := rc.Fetch(rdbKey, 60*time.Second, genDataFunc(expected, 200))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	err = rc.TagAsDeleted(rdbKey)
	assert.Nil(t, err)

	began = time.Now()
	nv := "value2"
	v, err = rc.Fetch(rdbKey, 60*time.Second, genDataFunc(nv, 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	v, err = rc.Fetch(rdbKey, 60*time.Second, genDataFunc("ignored", 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)

}

func TestWeakErrorFetch(t *testing.T) {
	rc := NewClient(rdb, NewDefaultOptions())

	clearCache()
	began := time.Now()

	fetchError := errors.New("fetch error")
	getFn := func() (string, error) {
		return "", fetchError
	}
	_, err := rc.Fetch(rdbKey, 60*time.Second, getFn)
	assert.Error(t, err)
	fetchError = nil
	_, err = rc.Fetch(rdbKey, 60*time.Second, getFn)
	assert.Nil(t, err)
	assert.True(t, time.Since(began) < time.Duration(150)*time.Millisecond)
}

func TestRawGet(t *testing.T) {
	rc := NewClient(rdb, NewDefaultOptions())
	_, err := rc.RawGet("not-exists")
	assert.Error(t, redis.Nil, err)
}

func TestRawSet(t *testing.T) {
	rc := NewClient(rdb, NewDefaultOptions())
	err := rc.RawSet("eeeee", "value", 60*time.Second)
	assert.Nil(t, err)
}

func TestLock(t *testing.T) {
	rc := NewClient(rdb, NewDefaultOptions())
	rc.Options.StrongConsistency = true
	owner := "test_owner"
	key := "test_lock"
	err := rc.LockForUpdate(key, owner)
	assert.Nil(t, err)
	err = rc.LockForUpdate(key, "other_owner")
	assert.Error(t, err)
	err = rc.UnlockForUpdate(key, owner)
	assert.Nil(t, err)
}
