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

func callLua(ctx context.Context, rdb redis.Scripter, script *redis.Script, keys []string, args []interface{}) (interface{}, error) {
	debugf("callLua: script=%s, keys=%v, args=%v", script.Hash(), keys, args)
	r := script.EvalSha(ctx, rdb, keys, args)
	if redis.HasErrorPrefix(r.Err(), "NOSCRIPT") {
		// try load script
		if err := script.Load(ctx, rdb).Err(); err != nil {
			debugf("callLua: load script failed: %v", err)
			r = script.Eval(ctx, rdb, keys, args) // fallback to EVAL
		} else {
			r = script.EvalSha(ctx, rdb, keys, args) // retry EVALSHA
		}
	}
	v, err := r.Result()
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
