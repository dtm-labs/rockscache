![license](https://img.shields.io/github/license/dtm-labs/rockscache)
![Build Status](https://github.com/dtm-labs/rockscache/actions/workflows/tests.yml/badge.svg?branch=main)
[![codecov](https://codecov.io/gh/dtm-labs/rockscache/branch/main/graph/badge.svg?token=UKKEYQLP3F)](https://codecov.io/gh/dtm-labs/rockscache)
[![Go Report Card](https://goreportcard.com/badge/github.com/dtm-labs/rockscache)](https://goreportcard.com/report/github.com/dtm-labs/rockscache)
[![Go Reference](https://pkg.go.dev/badge/github.com/dtm-labs/rockscache.svg)](https://pkg.go.dev/github.com/dtm-labs/rockscache)

简体中文 | [English](./README.md)

# RocksCache
首个确保缓存与数据库一致性的缓存库。

## 一致性问题
绝大多数应用为了降低数据库负载，增加并发，一般都会引入Redis缓存。引入缓存后，由于数据同时存储在两个地方：数据库和Redis，因此就存在分布式系统中的一致性问题。关于这个一致性问题的背景知识，以及Redis缓存流行方案介绍，可以参见：
- 这篇通俗易懂些：[聊聊数据库与缓存数据一致性问题](https://juejin.cn/post/6844903941646319623)
- 这篇更加深入：[携程最终一致和强一致性缓存实践](https://www.infoq.cn/article/hh4iouiijhwb4x46vxeo)

但是目前看到的所有缓存方案，都无法解决下面这个数据不一致的场景：

<img alt="scenario" src="https://pica.zhimg.com/80/v2-da95e008d2cd53d0e00e4a463e46b010_1440w.png" height=400 />

## 解决方案
本项目给大家带来了一个全新方案，能够确保缓存与数据库的数据一致性，解决这个大难题。此方案是首创，已申请专利，现开源出来，供大家使用。

此方案在流程上也采用先更新数据，再删除缓存，但是要求读取数据和删除缓存，都采用本库的操作进行。

### 读取数据代码
``` go
// 初始化一个redis客户端
rdb := redis.NewClient(&redis.Options{
  Addr:     "localhost:6379",
  Username: "root",
  Password: "",
})

// 使用默认选项生成rockscache的客户端
rc := rockscache.NewClient(rdb, NewDefaultOptions())

// 使用Fetch获取数据，第一个参数是数据的key，第二个参数为数据过期时间，第三个参数为缓存不存在时，数据获取函数
v, err := rc.Fetch("key1", 300, func()(string, error) {
  // 从数据库或其他渠道获取数据
  return "value1", nil
})
```

### 更新数据代码
更新数据和删除缓存是两个数据源的数据操作，无法采用简单的数据库事务等手段来保证两者都会完成。为了在更新数据后，能够确保删除缓存，有许多种做法（见前面的背景知识文章），这里我们推荐使用[dtm-labs/dtm](https://github.com/dtm-labs/dtm)的[二阶段消息](https://zhuanlan.zhihu.com/p/456170726)，因为它的使用在各种方案中是最简单的：

更新数据的代码，在`DoAndSubmitDB`中的回调函数中，更新数据库中的数据，然后会提交二阶段消息
``` go
  msg := dtmcli.NewMsg(DtmServer, shortuuid.New()).
    Add(BusiUrl+"/delayDeleteKey", &Req{Key: rdbKey})
  msg.WaitResult = true // wait for result. when submit returned without error, cache has been deleted
  err := msg.DoAndSubmitDB(BusiUrl+"/queryPrepared", db, func(tx *sql.Tx) error {
    return updateInTx(tx, value)
  })
```

二阶段消息的下一步操作是，删除缓存的代码，就是简单调用本库的`DelayDelete`
``` go
  app.POST(BusiAPI+"/delayDeleteKey", utils.WrapHandler(func(c *gin.Context) interface{} {
    body := MustMapBodyFrom(c)
    return rc.DelayDelete(body["key"].(string))
  }))
```

二阶段消息中回查数据库事务的代码，这部分就是 DTM 中的常规代码
``` go
  app.GET(BusiAPI+"/queryPrepared", utils.WrapHandler(func(c *gin.Context) interface{} {
    bb, err := dtmcli.BarrierFromQuery(c.Request.URL.Query())
    if err == nil {
      err = bb.QueryPrepared(db)
    }
    return err
  }))
```

上述源码的使用并不复杂，如果您想要进一步研究：
- 详细原理参考这篇文章：[dtm在缓存一致性中的应用](https://dtm.pub/app/cache.html)的相关章节
- 实际可运行的例子，参考[dtm-cases/cache](https://github.com/dtm-labs/dtm-cases/tree/main/cache)

当然这个库不仅可以搭载DTM，也能够搭载其他方案，例如本地消息表，事务消息等，参考[二阶段消息](https://zhuanlan.zhihu.com/p/456170726)中提到的对比方案，只需要做如下修改：
- 读取数据改为本库的`Fetch`
- 删除缓存改为本库的 `rc.DelayDelete(key)`

## 强一致的访问
如果您的应用需要使用缓存，并且需要的是强一致，而不是最终一致，那么可以通过打开选项`StrongConsisteny=true`来支持，访问方式不变
- 详细原理参考这篇文章：[dtm在缓存一致性中的应用](https://dtm.pub/app/cache.html)的相关章节
- 实际可运行的例子，参考[dtm-cases/cache](https://github.com/dtm-labs/dtm-cases/tree/main/cache)

## 降级以及强一致
本库支持降级，降级开关分为
- `DisableCacheRead`: 关闭缓存读，默认`false`；如果打开，那么Fetch就不从缓存读取数据，而是直接调用fn获取数据
- `DisableCacheDelete`: 关闭缓存删除，默认false；如果打开，那么DelayDelete就什么操作都不做，直接返回

当Redis出现问题，需要降级时，可以通过这两个开关控制。如果您需要在降级升级的过程中，也保持强一致的访问，rockscache也是支持的
- 详细原理参考这篇文章：[dtm在缓存一致性中的应用](https://dtm.pub/app/cache.html)的相关章节
- 实际可运行的例子，参考[dtm-cases/cache](https://github.com/dtm-labs/dtm-cases/tree/main/cache)

## 防缓存穿透
通过本库使用缓存，自带防缓存穿透功能。当`Fetch`中的`fn`返回空字符串时，认为这是空结果，会将过期时间设定为rockscache选项中的`EmptyExpire`.

`EmptyExpire` 默认为60s，如果设定为0，那么关闭防缓存穿透，对空结果不保存

## 防缓存击穿
通过本库使用缓存，自带防缓存击穿功能。一方面`Fetch`会在进程内部使用`singleflight`，避免一个进程内有多个请求发到Redis，另一方面在Redis层会使用分布式锁，避免多个进程的多个请求同时发到DB。

## 防缓存雪崩
通过本库使用缓存，自带防缓存雪崩。rockscache中的`RandomExpireAdjustment`默认为0.1，如果设定为600的过期时间，那么过期时间会被设定为`540s - 600s`中间的一个随机数，避免数据出现同时到期

## 联系我们
### 公众号
dtm官方公众号：分布式事务，大量干货分享，以及dtm的最新消息
### 交流群
请加 yedf2008 好友或者扫码加好友，验证回复 dtm 按照指引进群

![yedf2008](http://service.ivydad.com/cover/dubbingb6b5e2c0-2d2a-cd59-f7c5-c6b90aceb6f1.jpeg)

欢迎使用[rockscache](https://github.com/dtm-labs/rockscache)，欢迎star支持我们
