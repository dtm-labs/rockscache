![license](https://img.shields.io/github/license/dtm-labs/rockscache)
![Build Status](https://github.com/dtm-labs/rockscache/actions/workflows/tests.yml/badge.svg?branch=main)
[![codecov](https://codecov.io/gh/dtm-labs/rockscache/branch/main/graph/badge.svg?token=UKKEYQLP3F)](https://codecov.io/gh/dtm-labs/rockscache)
[![Go Report Card](https://goreportcard.com/badge/github.com/dtm-labs/rockscache)](https://goreportcard.com/report/github.com/dtm-labs/rockscache)
[![Go Reference](https://pkg.go.dev/badge/github.com/dtm-labs/rockscache.svg)](https://pkg.go.dev/github.com/dtm-labs/rockscache)

简体中文 | [English](https://github.com/dtm-labs/rockscache/blob/main/helper/README-en.md)

# RocksCache
首个确保最终一致、强一致的 `Redis` 缓存库。

## 特性
- 最终一致：极端情况也能确保缓存的最终一致
- 强一致：能够给应用提供强一致的访问
- 防击穿：给出更好的防击穿方案
- 防穿透
- 防雪崩

## 使用
本缓存库采用最常见的`更新DB后，删除缓存`的缓存管理策略

### 读取缓存
``` Go
import "github.com/dtm-labs/rockscache"

// 使用默认选项生成rockscache的客户端
rc := rockscache.NewClient(redisClient, NewDefaultOptions())

// 使用Fetch获取数据，第一个参数是数据的key，第二个参数为数据过期时间，第三个参数为缓存不存在时，数据获取函数
v, err := rc.Fetch("key1", 300, func()(string, error) {
  // 从数据库或其他渠道获取数据
  return "value1", nil
})
```

### 删除缓存
``` Go
rc.DelayDelete(key)
```

## 最终一致
引入缓存后，由于数据同时存储在两个地方：数据库和Redis，因此就存在分布式系统中的一致性问题。关于这个一致性问题的背景知识，以及Redis缓存流行方案介绍，可以参见：
- 这篇通俗易懂些：[聊聊数据库与缓存数据一致性问题](https://juejin.cn/post/6844903941646319623)
- 这篇更加深入：[携程最终一致和强一致性缓存实践](https://www.infoq.cn/article/hh4iouiijhwb4x46vxeo)

但是目前看到的所有缓存方案，如果不在应用层引入版本，都无法解决下面这个数据不一致的场景：

<img alt="scenario" src="https://pica.zhimg.com/80/v2-da95e008d2cd53d0e00e4a463e46b010_1440w.png" height=400 />

### 解决方案
本项目给大家带来了一个全新方案，能够确保缓存与数据库的数据一致性，解决这个大难题。此方案是首创，已申请专利，现开源出来，供大家使用。

当开发者读数据时调用`Fetch`，并且确保更新数据库之后调用`DelayDelete`，那么就能够确保缓存最终一致。当遇见上图中步骤5写入v1时，最终会放弃写入。
- 如何确保更新数据库之后会调用DelayDelete，参见[DB与缓存操作的原子性](https://dtm.pub/app/cache.html#atomic)
- 步骤5写入v1时，数据写入会被放弃的原理，参见[缓存一致性](https://dtm.pub/app/cache.html)

完整的可运行的例子，参考[dtm-cases/cache](https://github.com/dtm-labs/dtm-cases/tree/main/cache)

## 强一致的访问
如果您的应用需要使用缓存，并且需要的是强一致，而不是最终一致，那么可以通过打开选项`StrongConsisteny`来支持，访问方式不变
``` Go
rc.Options.StrongConsisteny = true
```

详细原理参考[缓存一致性](https://dtm.pub/app/cache.html)，例子参考[dtm-cases/cache](https://github.com/dtm-labs/dtm-cases/tree/main/cache)

## 降级以及强一致
本库支持降级，降级开关分为
- `DisableCacheRead`: 关闭缓存读，默认`false`；如果打开，那么Fetch就不从缓存读取数据，而是直接调用fn获取数据
- `DisableCacheDelete`: 关闭缓存删除，默认false；如果打开，那么DelayDelete就什么操作都不做，直接返回

当Redis出现问题，需要降级时，可以通过这两个开关控制。如果您需要在降级升级的过程中，也保持强一致的访问，rockscache也是支持的

详细原理参考[缓存一致性](https://dtm.pub/app/cache.html)，例子参考[dtm-cases/cache](https://github.com/dtm-labs/dtm-cases/tree/main/cache)

## 防缓存击穿
通过本库使用缓存，自带防缓存击穿功能。一方面`Fetch`会在进程内部使用`singleflight`，避免一个进程内有多个请求发到Redis，另一方面在Redis层会使用分布式锁，避免多个进程的多个请求同时发到DB，保证最终只有一个数据查询请求到DB。

本项目的的防缓存击穿，在热点缓存数据删除时，能够提供更快的响应时间。假如某个热点缓存数据需要花费3s计算，普通的防缓存击穿方案会导致这个时间内的所有这个热点数据的请求都等待3s，而本项目的方案，则能够立即返回。
## 防缓存穿透
通过本库使用缓存，自带防缓存穿透功能。当`Fetch`中的`fn`返回空字符串时，认为这是空结果，会将过期时间设定为rockscache选项中的`EmptyExpire`.

`EmptyExpire` 默认为60s，如果设定为0，那么关闭防缓存穿透，对空结果不保存

## 防缓存雪崩
通过本库使用缓存，自带防缓存雪崩。rockscache中的`RandomExpireAdjustment`默认为0.1，如果设定为600的过期时间，那么过期时间会被设定为`540s - 600s`中间的一个随机数，避免数据出现同时到期

## 联系我们
### 公众号
dtm-labs官方公众号：《分布式事务》，大量分布式事务干货分享，以及dtm-labs的最新消息
### 交流群
如果您希望更快的获得反馈，或者更多的了解其他用户在使用过程中的各种反馈，欢迎加入我们的微信交流群

请加作者的微信 yedf2008 好友或者扫码加好友，备注 `rockscache` 按照指引进群

![yedf2008](http://service.ivydad.com/cover/dubbingb6b5e2c0-2d2a-cd59-f7c5-c6b90aceb6f1.jpeg)

欢迎使用[dtm-labs/rockscache](https://github.com/dtm-labs/rockscache)，欢迎star支持我们
