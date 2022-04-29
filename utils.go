package rockscache

import (
	"log"
	"time"

	"github.com/go-redis/redis/v8"
)

var verbose = false

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

func callLua(rdb *redis.Client, script string, keys []string, args []interface{}) (interface{}, error) {
	debugf("callLua: script=%s, keys=%v, args=%v", script, keys, args)
	v, err := rdb.Eval(rdb.Context(), script, keys, args).Result()
	if err == redis.Nil {
		err = nil
	}
	return v, err
}
