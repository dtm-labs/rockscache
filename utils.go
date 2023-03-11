package rockscache

import (
	"context"
	"log"
	"runtime/debug"
	"time"

	"github.com/redis/go-redis/v9"
)

var verbose = false

// SetVerbose sets verbose mode.
func SetVerbose(v bool) {
	verbose = v
}

func debugf(format string, args ...interface{}) {
	if verbose {
		log.Printf(format, args...)
	}
}
func now() int64 {
	return time.Now().Unix()
}

type redisConn interface {
	Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd
}

func callLua(ctx context.Context, rdb redisConn, script string, keys []string, args []interface{}) (interface{}, error) {
	debugf("callLua: script=%s, keys=%v, args=%v", script, keys, args)
	v, err := rdb.Eval(ctx, script, keys, args).Result()
	if err == redis.Nil {
		err = nil
	}
	debugf("callLua result: v=%v, err=%v", v, err)
	return v, err
}

func withRecover(f func()) {
	defer func() {
		if r := recover(); r != nil {
			debug.PrintStack()
		}
	}()
	f()
}
