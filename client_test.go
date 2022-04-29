package rockscache

import (
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
		v, err := dc2.Fetch(rdbKey, 60, genDataFunc(expected, 200))
		assert.Nil(t, err)
		assert.Equal(t, expected, v)
	}()
	time.Sleep(20 * time.Millisecond)

	v, err := rc.Fetch(rdbKey, 60, genDataFunc(expected, 201))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	err = rc.DelayDelete(rdbKey)
	assert.Nil(t, err)

	nv := "value2"
	v, err = rc.Fetch(rdbKey, 60, genDataFunc(nv, 200))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)

	time.Sleep(300 * time.Millisecond)
	v, err = rc.Fetch(rdbKey, 60, genDataFunc("ignored", 200))
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
		v, err := dc2.Fetch(rdbKey, 60, genDataFunc(expected, 200))
		assert.Nil(t, err)
		assert.Equal(t, expected, v)
	}()
	time.Sleep(20 * time.Millisecond)

	v, err := rc.Fetch(rdbKey, 60, genDataFunc(expected, 200))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	err = rc.DelayDelete(rdbKey)
	assert.Nil(t, err)

	began = time.Now()
	nv := "value2"
	v, err = rc.Fetch(rdbKey, 60, genDataFunc(nv, 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	v, err = rc.Fetch(rdbKey, 60, genDataFunc("ignored", 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)

}
