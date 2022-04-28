package rockscache

import (
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
)

var rdbKey = "t1-id-1"

var redisOption = redis.Options{
	Addr:     "localhost:6379",
	Username: "root",
	Password: "",
}

var rdb = redis.NewClient(&redisOption)

func clearCache() {
	err := rdb.Del(rdb.Context(), rdbKey).Err()
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
func TestWeakFetch(t *testing.T) {
	dc, _ := NewClient(rdb, NewDefaultOptions())

	clearCache()
	began := time.Now()
	expected := "value1"
	go func() {
		v, err := dc.Fetch(rdbKey, 60, genDataFunc(expected, 200))
		assert.Nil(t, err)
		assert.Equal(t, expected, v)
	}()
	time.Sleep(20 * time.Millisecond)

	v, err := dc.Fetch(rdbKey, 60, genDataFunc(expected, 200))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	err = dc.Delete(rdbKey)
	assert.Nil(t, err)

	nv := "value2"
	v, err = dc.Fetch(rdbKey, 60, genDataFunc(nv, 200))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)

	time.Sleep(300 * time.Millisecond)
	v, err = dc.Fetch(rdbKey, 60, genDataFunc("ignored", 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)
}

func TestStrongFetch(t *testing.T) {
	clearCache()
	dc, _ := NewClient(rdb, NewDefaultOptions())
	dc.Options.StrongConsistency = true
	began := time.Now()
	expected := "value1"
	go func() {
		v, err := dc.Fetch(rdbKey, 60, genDataFunc(expected, 200))
		assert.Nil(t, err)
		assert.Equal(t, expected, v)
	}()
	time.Sleep(20 * time.Millisecond)

	v, err := dc.Fetch(rdbKey, 60, genDataFunc(expected, 200))
	assert.Nil(t, err)
	assert.Equal(t, expected, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	err = dc.Delete(rdbKey)
	assert.Nil(t, err)

	began = time.Now()
	nv := "value2"
	v, err = dc.Fetch(rdbKey, 60, genDataFunc(nv, 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)
	assert.True(t, time.Since(began) > time.Duration(150)*time.Millisecond)

	v, err = dc.Fetch(rdbKey, 60, genDataFunc("ignored", 200))
	assert.Nil(t, err)
	assert.Equal(t, nv, v)

}
