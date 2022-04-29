package rockscache

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBadOptions(t *testing.T) {
	assert.Panics(t, func() {
		NewClient(nil, Options{})
	})
}

func TestDisable(t *testing.T) {
	rc := NewClient(nil, NewDefaultOptions())
	rc.Options.DisableCacheDelete = true
	rc.Options.DisableCacheRead = true
	fn := func() (string, error) { return "", nil }
	_, err := rc.Fetch("key", 60, fn)
	assert.Nil(t, err)
	err = rc.DelayDelete("key")
	assert.Nil(t, err)
}

func TestEmptyExpire(t *testing.T) {
	testEmptyExpire(t, 0)
	testEmptyExpire(t, 10)
}

func testEmptyExpire(t *testing.T, expire int) {
	clearCache()
	rc := NewClient(rdb, NewDefaultOptions())
	rc.Options.EmptyExpire = expire
	fn := func() (string, error) { return "", nil }
	_, err := rc.Fetch("key1", 600, fn)
	assert.Nil(t, err)

	rc.Options.StrongConsistency = true
	_, err = rc.Fetch("key2", 600, fn)
	assert.Nil(t, err)
}

func TestErrorFetch(t *testing.T) {
	fn := func() (string, error) { return "", fmt.Errorf("error") }
	clearCache()
	rc := NewClient(rdb, NewDefaultOptions())
	_, err := rc.Fetch("key1", 60, fn)
	assert.Equal(t, fmt.Errorf("error"), err)

	rc.Options.StrongConsistency = true
	_, err = rc.Fetch("key2", 60, fn)
	assert.Equal(t, fmt.Errorf("error"), err)
}
