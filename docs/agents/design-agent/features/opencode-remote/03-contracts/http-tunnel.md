# C3 · HTTP / SSE / WebSocket 隧道帧协议

## 1. 兼容与连接建立

本协议名 `http-tunnel-v1`。旧 auth/hello、request/respond、Grok events/seq/replay/压缩契约不改字段语义，新能力在连接鉴权后的 capability 协商中声明。双方都声明 http-tunnel-v1 才可接受透明请求；能力缺失返回 503，不回退为旧单帧 respond。

hub 认证 hostId 和 kind 后生成新透明数据会话 connId；所有隧道帧都绑定该身份。connId 不是 Grok 的事件 seq，也不在重连后恢复。

| 底层传输 | 新透明通道的承载 |
|---|---|
| QUIC | 双方显式协商后，由 hub 为每条透明请求开启独立双向 QUIC stream，首帧是 http-open/ws-open；响应在同一 stream 回传。连接控制流继续承载鉴权、心跳和优先取消。 |
| WS 降级 | host 主动连接新的 WSS `/ws/host/tunnel` 数据连接；同一连接按 reqId 复用透明请求，不和旧 Grok events/respond 共用一个数据 writer。 |

WS 数据连接通过控制会话中取得的一次性 tunnelTicket 建立，ticket 绑定已认证 hostId 与控制会话，默认 30 秒过期；原子消费后生成 connId 并回复 tunnel-ready。host 是原生程序，可使用 Authorization 头提交 ticket。控制会话消失时关闭附属数据连接。数据连接断开后可在仍有效控制会话上请求新 ticket，但必须生成新 connId 并失败结束旧隧道，不续传旧字节。

QUIC 透明独立 stream 和 WS 新数据连接是协议扩展，hub 与共享库两侧都需要实施；现有 reqFrame.final 本身不能证明 hub 已支持该扩展。

### 控制面扩展字段

这些是新协议约定，并非现有源码已具备的字段：

| 控制消息 | 新增字段或操作 |
|---|---|
| 已有 auth/host登记 | kind、capabilities=["http-tunnel-v1"]；hostId仍由配对token身份校验 |
| 已有 hello | 可选tunnel对象：version=1、controlSessionId、initialWindowBytes、maxDecodedDataBytes、maxWireFrameBytes；QUIC另含已授权connId，WS另含初始tunnelTicket与expiresAt |
| tunnel-control / ticket-request | controlSessionId；只在已认证控制连接请求新WS数据票据 |
| tunnel-control / ticket-issued | controlSessionId、ticket、expiresAt；敏感字段，不能日志输出 |
| WSS数据入口的tunnel-ready | v=1、type=tunnel-control、op=tunnel-ready、controlSessionId、connId、tunnelVersion=1 |

未知能力由旧hub忽略，新host只有读到明确的tunnel协商结果才启用透明请求。配额/窗口和帧上限按双方支持的较小值协商；http-open/ws-open不能用更大initialWindows覆盖已协商上限。kind和能力不改变旧Grok帧含义。

## 2. 帧编码与公共字段

首版沿用易于接入的 JSON envelope，二进制 body/升级字节用 base64。QUIC 用现有形式的 4 字节大端长度前缀包裹 JSON；新增帧遵循本协议更小的长度上限，不因此缩小旧 Grok 帧限额。WS 数据连接每个 text message 承载一帧 JSON。新增透明帧暂不复用旧 events/respond 的 deflate 协商；浏览器内层 WS 的压缩字节可以原样经过隧道。

```json
{
  "v": 1,
  "type": "tunnel",
  "tunnelVersion": 1,
  "connId": "data-session-id",
  "reqId": "request-id",
  "op": "response-data",
  "lane": "response",
  "offset": 0,
  "payloadB64": "b2s="
}
```

这是协议载荷示例，不是实现代码。reqId 至少 128 bit 随机，当前 connId 内唯一。帧的主机归属由认证连接确定；不能按未校验的 payload.hostId 改换目标。`offset` 为该 lane 已发送的原始字节数，从 0 起连续递增；按解码后字节计数，不按 base64 字符长度。

JSON header 表示为多值条目列表，例如 `[{"name":"Set-Cookie","values":["a=1","b=2"]}]`，保留重复值与值的顺序。字段名须是 HTTP token，值拒绝 CR/LF/NUL，编码器必须保留合法 header octets，不得用有损 Unicode 替换；遇到不能用 JSON 文本无损表示的原始值，使用同条目的 `valuesB64` 字节表示，values 与 valuesB64 互斥。Cookie/Authorization 不写普通日志。

默认 decoded 数据块 ≤64 KiB；单个 JSON wire frame ≤256 KiB；单组 HTTP 头解码后 ≤64 KiB 且 ≤200 个字段值。超限、无效 base64、跳跃 offset、重复终帧等按 C4 处理。

## 3. HTTP 帧与方向

| op | 发送方向 | 字段/效果 |
|---|---|---|
| http-open | hub→host | method、target（path+raw query）、headers、可信 publicOrigin、initialWindows；创建请求，不含整块 body |
| request-data | hub→host | lane=request、offset、payloadB64；分块上传 |
| request-end | hub→host | 最终 offset、trailers；请求体 EOF，即使没有 body 也发送 |
| response-start | host→hub | 最终 status（非 101）、headers、声明 trailer 名称；只发送一次 |
| response-data | host→hub | lane=response、offset、payloadB64；所有响应均可分块，不依赖 Accept 识别 |
| response-end | host→hub | 最终 offset、trailers；正常 EOF，释放 HTTP 请求 |
| request-stop | host→hub | 原因 early_response；停止未完成上传，允许响应继续到 response-end |
| window-update | 收到数据的一方→发送方 | lane、ackOffset、limitOffset；增加指定方向可用窗口 |
| stream-error | 任一方→另一方 | code、受控 message、retryable；整条隧道失败，不是可重放承诺 |
| cancel / cancelled | 任一方→另一方 / 对方确认 | reason；幂等终止本次隧道 |

请求上传与响应下载可并行。不能等全部上传完成后才读响应，否则早期拒绝可能死锁。request-stop 是单方向停止；cancel 终止全部方向。host 不把浏览器断开翻译为 agent 的业务取消端点。

### 普通 GET 示例

```json
{"v":1,"type":"tunnel","tunnelVersion":1,"connId":"c1","reqId":"r1","op":"http-open","method":"GET","target":"/global/health","headers":[],"publicOrigin":"https://h-a1.hosts.example.com","initialWindows":{"request":262144,"response":262144}}
```

正常顺序：hub 发送 http-open、request-end；host 发送 response-start、零个或多个 response-data、response-end。hub 等到 response-start 后才向浏览器写最终 HTTP 状态和头；结束前的代理错误仍可返回 502/504。

### SSE 与大响应

SSE 与普通 HTTP 使用相同的帧序列。response-start 中的 Content-Type 可为 text/event-stream；每次读到可用字节就发 response-data，hub 及时写入并 flush。不等待凑满 64 KiB，不按 SSE event 解析或拆改 payload。普通二进制/静态资源也逐块回传，避免全量缓存。

HEAD 和不允许 body 的状态遵循 HTTP 语义，response-data 必须为空；Content-Length 可以表示 HEAD 对应实体的长度。其他固定 Content-Length 响应必须核对传递的字节数；失败时按中断处理，不能正常 EOF 假装完整。未知长度响应由 hub 选择 HTTP/1.1 分块或 HTTP/2 DATA。

首版由 hub 处理 Expect: 100-continue 的上传接收决定，不把 100 当最终 response-start；其他信息响应暂不透传，需在兼容性检查中确认官方前端不依赖 103 等。普通 trailer 声明在 response-start 传递，值在 response-end；HTTP/1.1 trailer 的分块头由 hub 生成。

## 4. WebSocket 升级与字节流

首版选择**握手透传、升级后原始字节转发**。Browser↔hub 和 host↔loopback 是不同 TCP/HTTP 连接段，但 hub/host 不成为两个重新协商的 WebSocket 应用端点。浏览器生成的 Sec-WebSocket-Key 透传到 opencode，opencode 的 Sec-WebSocket-Accept 经 hub 返回浏览器。这修正了上一轮说明中“两次独立 WS 握手必需”的表述。

| op | 方向 | 字段/效果 |
|---|---|---|
| ws-open | hub→host | target、headers（包括 Upgrade/Connection/Key/Version/Protocol/Extensions）、publicOrigin、ws-up/ws-down initialWindows |
| ws-ready | host→hub | status=101、上游握手 headers；后端协议升级成功，但 hub 尚需完整写回浏览器 101 |
| response-start/data/end | host→hub | 上游拒绝升级时返回实际 HTTP 状态/头/体，不能把所有 4xx 一律改成 502 |
| ws-data | 任一方→另一方 | lane=ws-up/ws-down、offset、payloadB64；仅在 ws-ready 后允许 |
| ws-end | 任一方→另一方 | 本方向 TCP EOF、最终 offset；不代表一个 WS closeCode |
| window-update / cancel / stream-error | 同 HTTP | 单方向窗口 / 整隧道失败清理 |

hub 验证 Cookie、Origin、WS version、key 等握手结构后发 ws-open；LocalProxy 以固定 loopback 目标、正确 Basic 和原浏览器 key 发起 HTTP/1.1 Upgrade。收到合法 101 后保留包括 Sec-WebSocket-Accept/Protocol/Extensions 在内的升级头；hub 验证 accept 与 key、协议选择相符，向浏览器写回 101，再转发升级流。

普通 HTTP 会剥离的 Upgrade/Connection 头在该分支专门处理。浏览器侧首版采用标准 HTTP/1.1 WS Upgrade；不要求 RFC 8441 的 HTTP/2 extended CONNECT。

ws-data 的 64 KiB 块可以跨越或切开一个 WS frame；接收端顺序写出字节即可，不按“1个WS帧=1个隧道帧”映射。浏览器 mask、消息 fragment、close 帧/closeCode、ping/pong 和协商过的压缩内容都在字节中保留。hub/host 不重写 mask、不另加 inner WS ping/pong、不解压业务帧；outer WSS 的心跳由数据连接自身维护。

host/hub 代理读取升级响应后必须转发其读缓冲内已预读的升级字节；hub 从 browser socket 读取时也不能丢弃预读数据。双方不能以只支持普通 ResponseWriter 的抽象实现升级，需显式支持升级连接句柄。

收到单侧 TCP EOF 时通知 ws-end 并半关闭对应目标写方向；另一方向允许短暂收尾，默认 5 秒后仍未结束则关闭两侧。协议的 closeCode 由真实 WS close 帧传递；没有 close 帧的掉线不伪造正常关闭码。

## 5. 应用窗口

每条隧道每个 lane 独立窗口，默认 256 KiB。初始 limitOffset=262144、ackOffset=0。发送方下一块的 offset+decodedLength 必须 ≤ 当前 limitOffset。

接收方实际向下游成功写入原始字节后更新 ackOffset，并发送绝对窗口 `limitOffset=ackOffset+windowSize`。窗口更新可合并，阈值为消耗 64 KiB 或距上次更新 20 ms，避免小块吞吐被窗口通知拖慢。limitOffset 单调不减；同连接重复更新不重复加额度。已确认指本段下游 write 成功，不等价于浏览器已呈现数据。

取消、错误、窗口更新不消耗 payload 窗口，走预留控制队列，避免窗口为零后双方无法解锁。QUIC 还具有底层流控；应用窗口提供跨 QUIC/WS 一致的内存边界。仅一个 lane 等待窗口不能持有全局 writer 锁或其他请求的资源锁。

## 6. 失败与状态

- HTTP 最终响应头尚未写出：本机连接失败为 502，后端响应头超时为 504；请求过大 413；认证/配额/离线错误优先在 hub 返回 401/403/429/503。
- 头已写出后：只终止该响应/流并记录失败；不能在已返回 200 的 SSE 后追加一个虚假的“504 HTTP响应”。HTTP/1.1 用异常断开，HTTP/2 用 stream reset，不以正常结束伪装完整数据。
- WS 101 前：后端实际拒绝状态原样返回；代理连接/无效握手返回 502，握手超时返回 504。101 后错误只能断开升级流。
- 收到不属于当前 connId 的帧或未知 reqId 的 data，不进入任何本机请求。合法取消竞争产生的迟到帧按 C4 墓碑规则丢弃，不能重建请求。

帧协议只转发结果，绝不承诺 exactly-once 执行；请求是否已在 opencode 产生副作用可能在断线后无法确定。

## 7. 规范依据与未来验证

HTTP 头、状态、方法、升级与重试语义参考 [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html)。WS 握手与线上帧格式参考 [RFC 6455](https://www.rfc-editor.org/rfc/rfc6455.html)。

未来验证：status/重复header/raw-query/binary/trailer 无损；所有响应可增量传输；Cookie/Basic 不串用；升级拒绝完整返回；大 WS frame 跨隧道块仍原样；QUIC 与新 WSS 数据连接行为一致。本轮仅定义协议，没有实施或运行验证。
