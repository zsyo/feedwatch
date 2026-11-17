# 并发与频率控制

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/rate-limiting.md)

<a id="rate-limiting"></a>

订阅链接多起来之后有两个风险：一轮开始瞬间对同一域名（最常见是 `github.com`）发出大量请求；以及 `per_feed` + 多渠道在一轮里连续打满 IM / 邮件 API。程序里做了三层约束：

| 层 | 配置（默认值） | 作用 |
| --- | --- | --- |
| 轮询周期 | `poll.interval: 30m` | 一个 feed 每轮最多请求 1 次；`Run()` 串行调用 `RunOnce`，两个周期绝不重叠。配合 ETag/304，未变化的 feed 只有一个 304 往返 |
| 抓取并发与限速 | `poll.concurrency: 4`、`poll.request_spacing: 100ms` | 有界 worker pool：最多 4 个 feed 同时在抓；两次请求**发起**至少隔 100ms，即全局约 10 req/s。单个慢 feed（含重试退避）不再阻塞排在后面的 feed |
| 通知限速 | `notify.send_spacing: 250ms` | **按渠道类型**各一个限速门：同一类型的多次投递至少隔 250ms（约 4 次/秒），不同服务互不阻塞。`Multi` 扇出本身就是串行的，`per_feed` 的多批次、多个同类型渠道都共用该类型的门 |

```yaml
poll:
  interval: 30m
  timeout: 2m
  concurrency: 4         # 1 = 完全串行
  request_spacing: 100ms # 0s 关闭
notify:
  send_spacing: 250ms    # 0s 关闭
```

几点说明：

- **原先是隐式全串行**（并发上限就是 1），既没有可配置项，慢 feed 也会拖住后面的 feed；现在改成显式、可调、有上界的。`poll.concurrency: 1` + 两个 spacing 设 `0s` 即可回到旧行为。
- 抓取限速是**全局**的，不按 host 分组；feed 分散在不同域名时偏保守，但上限明确、实现简单。抓取结果按 `feeds[]` 顺序写入各自下标，所以 diff、通知顺序、状态提交都与并发度无关。
- 通知限速按**渠道类型**分组：`email`、`telegram`、`wecom` …各有独立的一个门，某个服务被限流不会拖慢另一个。同一类型的所有渠道（例如两个邮件渠道）仍然共用该类型的门，所以一个 `per_feed` 批次同时投递给它们时不会连发。类型之间不互相等待，一条通知扇出到 N 种渠道也不会被叠加成 `N × spacing`。
- `N` 个 feed 的一轮最短耗时约 `N × request_spacing`。feed 极多时要保证它明显小于 `poll.timeout`（默认 2m），否则调大 `timeout` / 调小或关闭 spacing：超时后未抓完的 feed 本轮跳过、状态不提交，**不会误发通知**，但下一轮仍按 `feeds[]` 顺序从头开始，末尾的 feed 会长期轮不到。
- 状态写入使用独立的 10s 超时，不受 `poll.timeout` 影响：已经送达的批次不会因为本轮超时而丢掉提交、进而在下一轮重复推送。
- HTTP 层原有的保护仍在：`429` / `5xx`、网络错误按指数退避重试，并尊重 `Retry-After`（含 Telegram `parameters.retry_after`、`X-RateLimit-Reset`）。限速门负责**主动削峰**，退避负责**被动应对**。
- 各渠道自身还有频率上限（见[已知限制](limitations.md)）。`per_feed` + 几十个 feed + 多渠道时，必要时增大 `notify.send_spacing`。

---

[← 返回主文档](../README.md) · [English](en/rate-limiting.md)
