![license](https://img.shields.io/github/license/dtm-labs/rockscache)
![Build Status](https://github.com/dtm-labs/rockscache/actions/workflows/tests.yml/badge.svg?branch=main)
[![codecov](https://codecov.io/gh/dtm-labs/rockscache/branch/main/graph/badge.svg?token=UKKEYQLP3F)](https://codecov.io/gh/dtm-labs/rockscache)
[![Go Report Card](https://goreportcard.com/badge/github.com/dtm-labs/rockscache)](https://goreportcard.com/report/github.com/dtm-labs/rockscache)
[![Go Reference](https://pkg.go.dev/badge/github.com/dtm-labs/rockscache.svg)](https://pkg.go.dev/github.com/dtm-labs/rockscache)

简体中文 | [English](./README.md)

# RocksCache
首个确保缓存与数据库一致性的缓存库。

## 一致性问题
绝大多数应用为了降低数据库负载，增加并发，一般都会引入Redis缓存。引入缓存后，由于数据存储在两个地方，因此就存在分布式系统中的一致性问题。目前看到的现有的缓存方案，都无法解决下面这个数据不一致的场景：
<img alt="scenario" src="https://pica.zhimg.com/80/v2-da95e008d2cd53d0e00e4a463e46b010_1440w.png" height=250 />

本项目提供了一种彻底解决一致性问题的方案，示例代码如下：

### 读取数据代码
``` go
rdb := redis.NewClient(&redis.Options{
	Addr:     "localhost:6379",
	Username: "root",
	Password: "",
})

rc := rockscache.NewClient(rdb, NewDefaultOptions())
v, err := rc.Fetch("key1", 300, func()(string, error) {
  time.Sleep(1 * time.Second)
  return "value1", nil
})
```

### 更新数据代码
为了在更新数据后，能够确保删除缓存，有许多种做法，这里我们推荐使用DTM的二阶段消息，因为它的使用在各种方案中是最简单的：

更新数据的代码
``` go
	msg := dtmcli.NewMsg(DtmServer, shortuuid.New()).
		Add(BusiUrl+"/delayDeleteKey", &Req{Key: rdbKey})
	msg.WaitResult = true // wait for result. when submit returned without error, cache has been deleted
	err := msg.DoAndSubmitDB(BusiUrl+"/queryPrepared", db, func(tx *sql.Tx) error {
		return updateInTx(tx, value)
	})
```

删除缓存的代码
``` go
	app.POST(BusiAPI+"/delayDeleteKey", utils.WrapHandler(func(c *gin.Context) interface{} {
		req := MustReqFrom(c)
		err := rc.DelayDelete(req.Key)
		logger.FatalIfError(err)
		return nil
	}))
```

二阶段消息回查的代码
``` go
	app.GET(BusiAPI+"/queryPrepared", utils.WrapHandler(func(c *gin.Context) interface{} {
		bb, err := dtmcli.BarrierFromQuery(c.Request.URL.Query())
		logger.FatalIfError(err)
		return bb.QueryPrepared(db)
	}))
```

实际可运行的例子，参考[dtm-cases/cache](https://github.com/dtm-labs/dtm-cases/tree/main/cache)

当然这个库不仅可以搭载DTM，也能够搭载其他方案，参考[二阶段消息](https://zhuanlan.zhihu.com/p/456170726)中提到的对比方案，只需要调用 `dc.DelayDelete(key)` 删除缓存即可

## 强一致的访问
如果您的应用需要使用缓存，并且需要保证不会产生不一致读，那么参考这篇文章：[]()的相关章节

## 防缓存穿透
通过本库使用缓存，自带防缓存穿透功能。当`Fetch`中的`fn`返回空字符串时，认为这是空结果，会将过期时间设定为rockscache选项中的`EmptyExpire`.

`EmptyExpire` 默认为60s，如果设定为0，那么关闭防缓存穿透，对空结果不保存

## 防缓存击穿
通过本库使用缓存，自带防缓存击穿功能。一方面`Fetch`会在进程内部使用`singleflight`，避免一个进程内有多个请求发到Redis，另一方面在Redis层会使用分布式锁，避免多个进程的多个请求同时发到DB。

## 防缓存雪崩
通过本库使用缓存，自带防缓存雪崩。rockscache中的`RandomExpireAdjustment`默认为0.1，如果设定为600的过期时间，那么过期时间会被设定为`540s - 600s`中间的一个随机数，避免数据出现同时到期

## 降级
本库支持降级，降级开关分为
- `DisableCacheRead`: 关闭缓存读，默认`false`；如果打开，那么Fetch就不从缓存读取数据，而是直接调用fn获取数据
- `DisableCacheDelete`: 关闭缓存删除，默认false；如果打开，那么DelayDelete就什么操作都不做，直接返回

当Redis出现问题，需要降级时，可以通过这两个开关控制。这两个开关也用于强一致的访问方案