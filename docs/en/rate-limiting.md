# Concurrency & Rate Limiting

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../rate-limiting.md)

<a id="rate-limiting"></a>

As the number of subscribed links grows, two risks appear: a burst of requests to the same domain (`github.com` most often) at the start of a round, and `per_feed` + multiple channels saturating IM / email APIs consecutively within one round. The program imposes three layers of constraints:

| Layer | Setting (default) | Effect |
| --- | --- | --- |
| Polling period | `poll.interval: 30m` | Each feed is requested at most once per round; `Run()` calls `RunOnce` serially, so two periods never overlap. Combined with ETag/304, an unchanged feed costs only one 304 round trip |
| Fetch concurrency & pacing | `poll.concurrency: 4`, `poll.request_spacing: 100ms` | Bounded worker pool: at most 4 feeds fetching at once; two requests are **started** at least 100ms apart, i.e. about 10 req/s globally. One slow feed (including retry backoff) no longer blocks the ones behind it |
| Delivery pacing | `notify.send_spacing: 250ms` | **One rate-limit gate per channel type**: consecutive deliveries of the same type are at least 250ms apart (about 4/s), while different services do not block each other. `Multi` fan-out is serial by itself; `per_feed` batches and multiple channels of the same type all share that type's gate |

```yaml
poll:
  interval: 30m
  timeout: 2m
  concurrency: 4         # 1 = fully serial
  request_spacing: 100ms # 0s disables
notify:
  send_spacing: 250ms    # 0s disables
```

Notes:

- **It used to be implicitly fully serial** (concurrency effectively 1) with no setting to change it, and a slow feed held up the ones behind it; now it is explicit, tunable and bounded. `poll.concurrency: 1` with both spacings set to `0s` restores the old behavior.
- Fetch pacing is **global**, not grouped by host; it is conservative when feeds are spread across domains, but the ceiling is clear and the implementation simple. Results are written to their own `feeds[]` index, so diffing, notification order and state commits are independent of the concurrency level.
- Delivery pacing is grouped by **channel type**: `email`, `telegram`, `wecom`, … each has its own gate, so one service being throttled does not slow another. All channels of the same type (e.g. two email channels) still share that type's gate, so a `per_feed` batch delivered to both will not fire twice back-to-back. Types do not wait for each other either — fanning one notification out to N channel types is not multiplied into `N × spacing`.
- A round over `N` feeds takes at least about `N × request_spacing`. With very many feeds, keep that well below `poll.timeout` (default 2m), or raise `timeout` / lower or disable spacing: on timeout the unfinished feeds are skipped and no state is committed for that round, **no notification is sent by mistake**, but the next round starts again from the head of `feeds[]`, so feeds at the tail may never get a turn.
- State writes use a separate 10s timeout, unaffected by `poll.timeout`: batches already delivered do not lose their commit because the round timed out (which would duplicate them next round).
- The HTTP-layer protections remain: `429` / `5xx` and network errors retry with exponential backoff and honor `Retry-After` (including Telegram `parameters.retry_after` and `X-RateLimit-Reset`). The pacing gate does **proactive peak shaving**; backoff is the **reactive** counterpart.
- Each channel also has its own rate limit (see [Known Limitations](limitations.md)). With `per_feed` + dozens of feeds + many channels, increase `notify.send_spacing` as needed.

---

[← Back to main README](../../README_EN.md) · [简体中文](../rate-limiting.md)
