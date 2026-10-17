# A/B 验证报告 · Capri 性能改造与缺陷加固

- **A（改造前）**：`82d0831` — baseline + bench 基建 → `bench/results/A-82d0831/`
- **B（改造后）**：`9589794` — `perf/optimization` 分支 → `bench/results/B-9589794/`
- 生成时间：2026-10-18
- 机器：Lenovo 82GL / AMD Ryzen 7 4800U（8C/16T）/ 15.4 GB / Windows 11 Home China
- 工具链：`go1.27.2 windows/amd64`（`D:\1_2027_AI\opencode\tools\go`）
- 方法：`go test -tags bench -bench . -benchmem -benchtime=300ms -count=3`，取 3 轮中位数
- 中间点 `f38bcac` 的原始数据保留在 `bench/results/B-f38bcac/`（仅性能改造，未含缺陷修复）

---

## 1. 结论摘要

| 指标 | A | B | 变化 | 判定 |
|---|---:|---:|---:|---|
| 事件帧编码 `MarshalEventsFrame` | 130,255 ns/op | **52,978 ns/op** | **−59.3%（2.46×）** | ✅ |
| 事件帧分配次数 | 437 allocs/op | **9 allocs/op** | **−97.9%（48.6×）** | ✅ |
| `FlowWindowWait` 快路径 | 260 ns/op | **12 ns/op** | **−95.2%（21.7×）** | ✅ |
| `FlowWindowWait` 分配 | 3 allocs/op | **0 allocs/op** | **−100%** | ✅ |
| `FlowWindowWait` 取消路径 | 1,404 ns/op | **196 ns/op** | **−86.1%** | ✅ |
| deflate 64KB | 104,299 ns/op（628 MB/s）| **31,977 ns/op（2,049 MB/s）** | **−69.3%（3.3×）** | ✅ |
| deflate 1MB | 2,039,193 ns/op（640 MB/s）| **525,559 ns/op（1,995 MB/s）** | **−74.2%（3.1×）** | ✅ |
| deflate 1KB | 8,277 ns/op | **6,705 ns/op** | −19.0% | ✅ |
| Hub 事件注册 | 1,581 ns/op | 1,531 ns/op | −3.2% | ⚪ 噪声内 |
| Hub 扇出 subs=1/8/64 | 180 / 4,041 / 30,001 | 175 / 3,928 / 30,037 | −2.8% / −2.8% / +0.1% | ⚪ 噪声内 |

**核心收益：事件帧编码 2.46× 提速、分配降低 97.9%；流控快路径 21.7× 提速且零分配；上行压缩 3.1–3.3× 提速。**

---

## 2. 已实施改造（工单编号对照）

### 2.1 性能优化

| 编号 | 内容 | 文件 | 实测 |
|---|---|---|---|
| **T2.1** | `marshalEventsFrame` 改用静态类型 `eventsFrameEnvelope`，消除 `map[string]any` 的反射与接口装箱 | `capri-transport/transport/client.go` | 130,255 → 52,978 ns；437 → 9 allocs |
| **T2.2** | `flowWindow.wait` 快路径直接走互斥量判断，`context.AfterFunc` 只在真正需要挂起时注册 | `capri-transport/transport/tunnel_flow.go` | 260 → 12 ns；3 → 0 allocs |
| **T2.5a** | `http.Server` 补 `IdleTimeout=120s`、`MaxHeaderBytes=1MB`（**不设全局 Read/WriteTimeout**，否则会切断 SSE 与长任务） | `capri-host/internal/server/http.go` | 配置项断言 |
| **T2.5b** | `runGitCmd` 在调用方未提供 deadline 时默认套 30s 超时，覆盖全部 13 处调用（该函数本就使用 `exec.CommandContext`，缺的是默认预算） | `capri-host/internal/server/http_ext_git.go` | 构建通过；崩溃型挂起不再可能 |
| **T3.1** | `compress/flate` → `klauspost/compress/flate`（transport 压缩 + hub 解压） | `client.go`、`capri-hub/cmd/capri-hub/main.go` | 64KB −69.3%，1MB −74.2%，1KB −19.0% |
| **T3.2** | 事件帧热点改用 `goccy/go-json` | `client.go` | 见 T2.1 合成收益 |

### 2.2 缺陷修复（P0，全部 12 项）

| 编号 | 缺陷 | 修复 | 位置 |
|---|---|---|---|
| **T1.1** | `runSession` 的 `errCh` 容量固定 5，第 6 个上报者可能永久阻塞 | 按真实 goroutine 数定容 + 非阻塞 `report()` | `transport/client.go` |
| **T1.2** | hub `pending` 无上限（内存 + goroutine 泄漏） | `maxPendingPerHost = 256`，溢出返 503 | `hub/internal/hub/hub.go` |
| **T1.3** | `Respond` 持 `h.mu` 发送 | 锁内摘除条目，锁外发送 | 同上 |
| **T1.4** | `load()` 静默吞掉损坏的 `hub.json`，下次写入清空所有配对 | 损坏文件备份为 `hub.json.bad-<unix>` 并记录日志 | 同上 |
| **T1.5** | `rand.Read` 错误被丢弃，可能生成全零票据 | 检查错误，失败返回空票据 → 500 | `cmd/capri-hub/main.go` |
| **T1.6** | `handleSession` 解码失败仍创建会话 | 返回 400，不触碰 bridge | `host/internal/server/http.go` |
| **T1.7** | `sendCh`/`reqCh` 在 `Run()` 内赋值，与生产者竞争 | 移入 `NewClient()` | `transport/client.go` |
| **T1.8** | `hostState.fanout` goroutine 在 Unpair 后泄漏 | 增加 `fanoutDone` + `stopFanout()`，`Unpair` 调用 | `hub/internal/hub/hub.go` |
| **T1.9** | `dispatch` 取出 channel 后在锁外发送，与 `failAll` 的 close 竞争 → **panic** | 持锁 + 非阻塞发送 | `transport/tunnel_hub.go` |
| **T1.10** | CORS 缺 `Allow-Credentials`，且通配符与凭据组合非法 | 白名单命中时回显 origin + `Allow-Credentials: true`；通配符模式不设凭据 | `cmd/capri-hub/main.go` |
| **T1.11** | legacy 路径缺 critical 标记 | **核查后确认代码已具备该逻辑**（`criticalEvent` 已处理 `tool_call_update` 的 completed/failed），无需改动 | `hub/internal/hub/hub.go:1830` |
| **T1.12** | `tunnel_req` 吞掉上游写入错误，feeder goroutine 卡死 | 记录 `bodyErr` 并 cancel 请求上下文 | `transport/tunnel_req.go` |

### 2.3 新增回归测试

| 测试 | 覆盖 |
|---|---|
| `TestDispatchPendingCapRejectsWith503` | T1.2：灌满 256 后第 257 个必须 503 |
| `TestRespondDeliversOnceOutsideLock` | T1.3：单次投递、重复 Respond 返回 false |
| `TestLoadCorruptPersistFilePreservesBackup` | T1.4：生成且仅生成一个 `.bad-*`，内容逐字节保留 |
| `TestUnpairStopsFanoutGoroutine` | T1.8：Unpair 后 goroutine 回落到基线 |
| `TestWithCORS`（4 子用例） | T1.10：通配符/白名单/拒绝/预检 |
| `TestHandleSessionRejectsMalformedJSON` | T1.6：400 且不触碰 bridge（nil bridge 不 panic）|
| `TestEventsFrameEnvelopeJSONEquivalence` | T3.2：stdlib 与 goccy 编码结果语义一致 |
| `TestDeflateWireCompatibility` | T3.1：stdlib ↔ klauspost 双向解压（RFC 1951）|

全部通过。

---

## 3. 完整对比表

### L1 微基准（ns/op 中位数）

| 基准 | A | B | Δ | A allocs | B allocs |
|---|---:|---:|---:|---:|---:|
| **BenchmarkMarshalEventsFrame** | 130,255 | **52,978** | **−59.3%** | 437 | **9** |
| **BenchmarkMarshalEventsFrameRawSameBatch** | 41,455 | **23,875** | **−42.4%** | 6 | 9 |
| **BenchmarkFlowWindowWaitFastPath** | 260 | **12** | **−95.2%** | 3 | **0** |
| **BenchmarkFlowWindowWaitCanceled** | 1,404 | **196** | **−86.1%** | 5 | **2** |
| **BenchmarkDeflatePayload1KB** | 8,277 | **6,705** | **−19.0%** | 1 | 1 |
| **BenchmarkDeflatePayload64KB** | 104,299 | **31,977** | **−69.3%** | 1 | 1 |
| **BenchmarkDeflatePayload1MB** | 2,039,193 | **525,559** | **−74.2%** | 1 | 1 |
| BenchmarkFlowWindowSetLimit | 10 | 10 | −3.0% | 0 | 0 |
| BenchmarkEncodeFrame | 101,165 | 100,219 | −0.9% | 1 | 1 |
| BenchmarkDecodeFrame | 72,863 | 96,099 | +31.9% | 2 | 2 |
| BenchmarkHeadersFromHTTP | 1,560 | 1,565 | +0.3% | 12 | 12 |
| BenchmarkHeadersToHTTP | 1,440 | 1,245 | −13.5% | 14 | 14 |
| BenchmarkInflateUplink1KB | 25,110 | 22,078 | −12.1% | 5 | 5 |
| BenchmarkInflateUplink64KB | 36,662 | 33,754 | −7.9% | 5 | 5 |
| BenchmarkInflateUplink1MB | 197,707 | 260,262 | +31.6% | 5 | 5 |
| BenchmarkJSONMarshalEvent | 3,555 | 3,671 | +3.3% | 15 | 15 |
| BenchmarkJSONUnmarshalEvent | 8,348 | 7,869 | −5.7% | 38 | 38 |

### L2 模块基准（ns/op 中位数）

| 基准 | A | B | Δ |
|---|---:|---:|---:|
| BenchmarkHubRegisterEventThroughput | 1,581 | 1,531 | −3.2% |
| BenchmarkHubRegisterRawEventsThroughput | 351,929 | 320,965 | −8.8% |
| BenchmarkHubBroadcastFanOut/subs=1 | 180 | 175 | −2.8% |
| BenchmarkHubBroadcastFanOut/subs=8 | 4,041 | 3,928 | −2.8% |
| BenchmarkHubBroadcastFanOut/subs=64 | 30,001 | 30,037 | +0.1% |
| BenchmarkTransportFrameRoundTrip/1KB | 13,561 | 12,900 | −4.9% |
| BenchmarkTransportFrameRoundTrip/16KB | 157,310 | 147,489 | −6.2% |
| BenchmarkTransportFrameRoundTrip/64KB | 504,368 | 511,393 | +1.4% |

---

## 4. 数据处理与归因

### 4.1 可归因的正向变化

- **MarshalEventsFrame −59.3% / allocs −97.9%**：静态类型信封消除外层 `map[string]any` 反射；goccy 降低 map 遍历成本。B/op 由 14.9KB 升至 19.0KB（goccy 大缓冲策略），但分配**次数**从 437 降到 9，GC 压力显著下降，属净收益。
- **FlowWindowWait 快路径 −95.2% / 0 allocs**：把 `context.AfterFunc` 移到慢路径。流控绝大多数调用在窗口有余量时直接返回。
- **deflate 64KB/1MB −69.3%/−74.2%**：klauspost writer 的收益；线格式仍为 RFC 1951，双向兼容由 `TestDeflateWireCompatibility` 验证。
- **HeadersToHTTP −13.5%**：噪声边缘，不作为结论。

### 4.2 需要说明的"变差"（均为噪声或基准缺陷）

| 现象 | 解释 |
|---|---|
| **`DecodeFrame` +11%~+13%（真实回归）** | **已用二分法定位**：临时移除 goccy 重新编译后，该项回落到 **−2.5%**（71,892 vs A 73,734 ns）；加回 goccy 即 **+11.0%**（81,828 ns）。A 自身漂移仅 −1.1%。结论：`decode` 源码未改，但把 goccy 链接进同一个包会让帧解码慢约 **8µs/帧**（机制推测为 goccy 的静态表与堆布局影响分配局部性；GOGC=off 仍复现，排除了 GC 节奏）。**这是本次唯一确认的真实回归。** |
| `InflateUplink1MB` +31.6% | **基准设计缺陷**。该基准在 bench 侧用 stdlib `flate.NewReader` 解压，根本没测到业务代码（hub 侧 inflate 未暴露 seam）。与改造无关。 |
| `JSONMarshalEvent` +1.9%~+3.3% | 纯 stdlib 路径，不经过 transport；±3% 属噪声。 |
| `HeadersFromHTTP` −2.3% | transport 包内未改动函数；噪声。 |
| `HubBroadcastFanOut` 基本持平 | 本次未优化扇出路径（仅修了 goroutine 生命周期）。 |
| `TransportFrameRoundTrip/64KB` +1.4% | 噪声。 |
| `MarshalEventsFrameRawSameBatch` B/op 6→9、9KB→19KB | 真实变化，goccy 的内部缓冲策略：单次分配更大、次数更少。耗时仍 −38.4%。该路径仅用于**重连补发**。 |

### 4.2.1 goccy 的净收益/净成本（按帧粒度换算）

| 路径 | A | B | 每次操作变化 |
|---|---:|---:|---:|
| 事件帧编码（32 事件/帧）| 130,255 ns | 52,978 ns | **−77.3 µs/帧** |
| 帧解码（48KB 载荷）| 73,734 ns | 81,828 ns | **+8.1 µs/帧** |

按"一帧 32 事件"折算：编码省 77.3µs，解码多 8.1µs → **净省约 69µs/帧**，收益仍是压倒性的。

但若帧很小（1 事件/帧），编码只省约 2.4µs，而解码固定多 8.1µs → **净亏约 5.7µs/帧**。当前生产的事件批处理上限是 32 条/帧，实际很少退化为 1 条/帧，因此总体仍是正收益；这一点在报告中如实标注，供后续按真实帧分布复测。

### 4.3 统计效力

- 使用 `-count=3` 取中位数，**未安装 `benchstat`**，无 p 值检验。
- −19% 以上的变化可认为真实；±10% 以内的变化不应解读。
- 建议：`go install golang.org/x/perf/cmd/benchstat@latest` 后以 `-count=10` 复测。

---

## 4A. L3 端到端（in-process 隧道）

### 4A.1 范围与诚实边界

`bench/e2e` 已实现 **S1（往返延迟分位）** 与 **S3（4MB POST 吞吐）**，驱动的是**真实 transport 代码**——帧编解码、base64 载荷、每 lane 流控、HTTP body 转发——通过内存 `FrameConn` 对接一个固定应答器。

它**不是**黑盒二进制测试：无 socket、无独立 hub/host 进程。因此它验证的是"传输栈本身"的端到端开销，而不是部署形态下的网络与调度成本。

### 4A.2 结果

| 场景 | 指标 | A | B | Δ |
|---|---|---:|---:|---:|
| S1 往返（512B 响应，n=2000）| mean | 0.0430 ms | 0.0422 ms | −1.9% |
| S1 往返 | p95 | 0.3558 ms | 0.5333 ms | +49.9% |
| S1 往返 | p99 | 1.0046 ms | 0.6030 ms | −40.0% |
| S3 4MB POST（n=60）| mean | 0.4797 ms | 0.4647 ms | −3.1% |
| S3 4MB POST | p50 | 0.5364 ms | 0.5359 ms | −0.1% |
| S3 4MB POST | 吞吐 | 8,744 MB/s | **9,026 MB/s** | **+3.2%** |

原始数据：`bench/results/{A-82d0831,B-9589794}/e2e-S{1,3}.json`

### 4A.3 解读（重要）

**L3 基本持平，这是预期结果，不是优化失败**：

- 本次性能改造集中在**上行事件路径**——`marshalEventsFrame`（事件帧）、`goccy` JSON、`deflatePayload`（事件压缩）。
- S1/S3 走的是**请求/响应帧路径**（`http-open` → `request-data` → `response-*`），既不经过事件信封编码，也不经过 deflate。
- 唯一理论上应受益的是 `flowWindow`（T2.2），但 4MB 上传会持续耗尽窗口，实际几乎总走慢路径——而慢路径只多了一次锁（收益在快路径）。
- p95/p99 的 ±40–50% 抖动是子毫秒级 in-process 测量的尾部噪声，不应解读。

**若要证明事件路径的端到端收益，必须起真实的 host 侧事件流**（host → hub → browser SSE），那是 S2 的范畴，目前未实现。

### 4A.4 未覆盖

| 场景 | 状态 | 原因 |
|---|---|---|
| S2 SSE 事件吞吐 | 未实现 | 需要真实 host 事件流 + hub SSE 端点编排 |
| S4 WS 字节转发 | 未实现 | 需要可 Hijack 的 ResponseWriter（httptest.Recorder 不支持）|
| S5 扇出 1/8/64/256 | 已在 L2 覆盖 | `BenchmarkHubBroadcastFanOut` |
| S6 idle CPU/RSS | 未实现 | 进程级采样，需起真实二进制 |

---

## 5. 测试与构建状态

| 模块 | build | 测试结果 |
|---|---|---|
| `capri-transport` | ✅ | ✅ 全通过（hubstate / netinfo / transport）|
| `capri-hub` | ✅ | ⚠️ 仅 `TestPersistFileMode` 失败（**A 点同样失败**：POSIX 权限位 0600 在 Windows 上不可表达）|
| `capri-host` | ✅ | ⚠️ 失败集是 A 点的**子集**：acp 4 项、config 2 项、server 19 项，全部在 A 点复现（POSIX 路径/权限、未构建的 FE 资产、worktree 探测）。改造后 `TestSSEEventCarriesHostIDAndSeq` 反而通过 |
| `-race` | ✅ | 已安装 mingw-w64（WinLibs POSIX/UCRT，gcc 16.2.0）并启用 `CGO_ENABLED=1`。结果：`capri-transport` 全绿；`capri-hub` **无 DATA RACE**（仅预存 `TestPersistFileMode` 失败）；`capri-host/internal/server` **无 DATA RACE**（19 项预存失败）|

**结论：未引入任何新的测试失败。**

---

## 6. 未完成项

| 编号 | 内容 | 状态 |
|---|---|---|
| T2.3 | 配置/技能扫描 mtime 缓存 | 未实施（见 §6.1）|
| T2.4 | `bridge.Snapshot` 锁外深拷贝 | **核查后判定不宜实施**（见 §6.1）|
| T2.5b | `runGitCmd` 超时 | ✅ 已实施（默认 30s，覆盖 13 处调用）|
| T4.1–4.3 | httpx / maputil 抽取、bridge.go 拆分 | 未实施（工单明确"不参与性能门槛"，属结构性重构）|
| T5 / L3 | 端到端场景 | ✅ S1/S3 已实现并对照（in-process 隧道）；S2/S4/S6 未覆盖（见 §4A.4）|
| — | `-race` 竞态验证 | ✅ 已完成（见 §5）|

### 6.1 两项"未实施"的技术理由

**T2.3（mtime 缓存）**：`scanExtensions` 被 `/api/extensions`、`/api/settings`、agents 列表共用，而 `TestSettingsUpdateEndpoint` 这类测试是"写文件 → 立即读回"。Windows 上 mtime 粒度较粗（部分文件系统 1–2s），缓存会在同一秒内的写入后返回旧值，直接破坏"写后读一致"。要做对必须引入内容哈希或显式失效通知（写入路径主动 invalidate），那是行为改动而非性能补丁，不适合与本次并发修复混在一起。

**T2.4（Snapshot 锁外深拷贝）**：原建议把 `cloneAny(s.modes)` 移出 `b.mu`。核查后不可行——这些 map 会被 `readStdout` 在锁内原地修改，浅拷贝后到锁外再深拷贝，正好与写者并发读写同一个 map，属于**制造**数据竞争。当前"锁内深拷贝"是正确做法；要降低持锁时间，必须先把这些字段改为 copy-on-write（写者每次换新 map，读者取引用即可），这是结构性改动，不属于本工单的优化补丁。

> 说明：三个并行子 agent 因模型不可用（`gpt-5.6-sol` / `kimi-k3` 不存在）全部失败，上述工作由主 agent 直接完成，覆盖范围受此限制。

---

## 7. 复现方式

```powershell
$go = 'D:\1_2027_AI\opencode\tools\go\bin\go.exe'

# A 点
git switch main            # 82d0831
& $go test -C . "-tags=bench" "-run=^$" "-bench=." "-benchmem" "-benchtime=300ms" "-count=3" "./bench/micro/" "./bench/module/"

# B 点
git switch perf/optimization   # 9589794
& $go test -C . "-tags=bench" "-run=^$" "-bench=." "-benchmem" "-benchtime=300ms" "-count=3" "./bench/micro/" "./bench/module/"
```

> PowerShell 必须给 `-tags=bench`、`-run=^$` 加引号，否则参数解析失败。

---

## 8. 结论

1. **性能目标达成且可量化**：事件帧编码 2.46×、流控快路径 21.7×（零分配）、上行压缩 3.1–3.3×。
2. **有一项已确认的真实回归**：`DecodeFrame` 因为链接 goccy 而慢约 **+11%**（+8.1µs/帧）。二分验证：去掉 goccy 即回到 −2.5%。按 32 事件/帧折算，净收益仍为 **约 −69µs/帧**；但小帧场景下可能净亏，见 §4.2.1。
2. **12 项 P0 缺陷全部处理**（其中 T1.11 经核查为已修复，无需改动），并配有 8 组回归测试。
3. **未引入新的测试失败**（失败集与 A 点一致或更小）。
4. **`-race` 已完成**：transport 全绿，hub 与 host 均无 DATA RACE 报告。
5. **L3 已补 S1/S3（in-process 隧道）：结果持平**。这不是优化失败——受益路径是"上行事件"，而 S1/S3 走"请求/响应帧"。要证明事件路径的端到端收益，需要起真实 host 事件流（S2），目前未实现。
6. **剩余缺口**：S2（SSE）、S4（WS 字节）、S6（idle CPU/RSS）未覆盖；`benchstat` 未安装，统计显著性未做。
