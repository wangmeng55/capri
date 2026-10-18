# shared-transport · 连接、透明通道与流控设计

> 设计修订稿，具体wire格式以[C3](03-contract-http-tunnel.md)、生命周期以[C4](03-contract-stream-lifecycle.md)为准。尚未实现。

## 1. 职责与抽取边界

复用host出站连接、配对、网络探测和状态工具；新增agent无关的HTTP/升级流传输。Grok继续走原事件与单帧respond通道。流式字节保序且不丢弃；共享库不解析opencode端点、不读取opencode密码、不包含acp.Bridge等具体agent类型。

旧事件来源通过capri-host里的适配器向共享库提供中立载荷和原seq；不改变旧事件seq分配、可丢事件选择和replay行为。新的HTTP/WS字节不进入旧事件replay。

## 2. module布局计划

独立共享module计划使用公开包transport/、hubstate/、netinfo/、procattr/，其内部实现可放该module自己的internal目录。具体module路径须先确定；原capri-host module为github.com/AgentsHarness/capri-host，不能让独立仓库直接导入其internal/transport。

两个host入口首版保留在capri-host repo：cmd/capri-host与cmd/opencode-host。capri-hub作为另一repo使用共享协议包。上述是设计布局，不是本轮创建的目录或Go module。

## 3. 组件与概念接口

| 组件/操作 | 输入 | 输出与责任 |
|---|---|---|
| HubClient.Run | context、中立配置、旧事件适配器、透明请求Handler | 出站连接、鉴权、能力协商、重连状态 |
| TunnelSession.OpenHTTP | connId/reqId、HTTP元数据和请求体lane | response-start、可读body/trailer；取消句柄 |
| TunnelSession.OpenUpgrade | WS升级请求元数据 | 拒绝HTTP响应或升级后双向字节句柄 |
| LocalHandler | 经过认证的新透明请求 | 调用host装配的本机代理，不内置Basic |
| FrameCodec.Encode/Decode | C3帧 | 长度校验、headers多值、base64原字节、offset |
| FlowController | 每lane窗口与消费进度 | 独立credit、有界缓存、等待/取消 |
| SessionRegistry | connId/reqId/context | 唯一归属、终止竞争、断线批量清理 |

接口为设计语义，实际Go签名在实现时确定。旧文档的Enqueue(reqId,payload,final)不足以表达response头、上传、取消、窗口和upgrade，替换为上述完整session/帧模型。

## 4. HTTP执行

收到http-open后创建请求context与分块body；流式调用host提供的LocalHandler。不等待全部上传结束才读响应。response-start传最终状态和多值头，response-data增量回传，response-end传EOF/trailer。所有响应都支持流式，SSE不靠一个请求Accept值切换到特殊缓存模式。

读取body/socket时尊重对应lane窗口；无credit则停止读取上游。取消关闭body/连接，释放缓冲与注册表项。若response头已提交给浏览器，后续失败只报告流中断，不能追加504响应。

## 5. WebSocket执行

收到ws-open后调用支持Upgrade的LocalHandler，以浏览器key和升级元数据获得上游101；失败走普通HTTP拒绝分支。成功回ws-ready，升级后的原始字节使用ws-up/ws-down独立窗口。

64 KiB是隧道块，可能跨WS frame边界；不解码mask、ping/pong或业务消息。closeCode自然存在于真实WS close字节中；取消/断线不伪造closeCode。

## 6. 传输与隔离

QUIC协商新能力后每透明请求独立双向stream；WS降级使用新/ws/host/tunnel连接，不把新增数据塞进旧Grok事件writer。WS连接内部公平复用reqId；控制队列与payload窗口分开。Grok旧通道的压缩、响应限额不自动施加到新通道。

认证不成功、能力不足或错误connId的帧不分派给LocalHandler。安全取消竞态的迟到帧按C4墓碑处理。网络重连重新创建透明session，不恢复旧请求。

## 7. 未来验收场景

普通GET/POST、重复头/raw-query/binary、分块上传和早期响应、SSE增量、WS握手拒绝与raw字节、credit耗尽与公平性、取消/超时/到期/断线、模块边界和Grok回归。具体用例是未来工作，本轮未编写测试或实施代码。
