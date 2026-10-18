# opencode-remote · 四份契约

日期：2026-10-14。状态：设计修订稿；尚未实现、尚未通过端到端验证。

本轮根据用户要求补齐可实施的设计。共享传输库、双 host 二进制、按 kind 分叉、官方 opencode 前端四项既定方向保持有效。下面的域名、票据、帧协议和默认限额是本轮具体设计选择，尚未被用户逐项确认；默认参数是起始配置，不是实测性能结论。

| 契约 | 回答的问题 | 文件 |
|---|---|---|
| C1 Origin 与 hub 路由 | 域名如何找到 host，前端由谁提供 | [origin-routing.md](03-contract-origin-routing.md) |
| C2 浏览器认证 | 登录如何覆盖普通 HTTP、SSE、WS 和 iframe | [browser-auth.md](03-contract-browser-auth.md) |
| C3 HTTP 隧道协议 | 请求、响应头、字节块、Upgrade 如何过长连接 | [http-tunnel.md](03-contract-http-tunnel.md) |
| C4 生命周期与流控 | 取消、背压、超时、断线和重放如何处理 | [stream-lifecycle.md](03-contract-stream-lifecycle.md) |

详细时序图是设计文档的一部分，集中在 [交互链路](04-interactions.md)，总体设计通过链接引用，避免维护两份不同流程。

## 术语

- hub：部署在公网可达服务器上的 capri-hub 服务。首版一个实例处理主机登记、浏览器认证和中继；服务器不运行被控机器的 agent。
- host：被控机器上的进程，主动连到 hub；capri-host 驱动 Grok，opencode-host 管理本机 opencode serve。
- origin：协议 + 域名 + 端口，例如 `https://h-a1.hosts.example.com:443`。
- hostOrigin：hub 为某台 opencode 主机分配的浏览器访问 origin；地址指向 hub，不是被控机器的公网地址。
- connId：一次 host↔hub 连接的随机身份；重连后变化。
- reqId：connId 范围内一次 HTTP 请求或 WS 隧道的随机身份；不能用于跨连接续传。
- lane：一次隧道内独立流控方向，request / response / ws-up / ws-down。

## 权威关系

需求决定目标与范围；总体设计决定组件分工；本目录决定新透明通道的具体协议。旧 Grok request/respond、事件 seq/replay 协议继续沿用。若旧模块摘要与本目录不一致，以本目录中新透明通道的定义为准，并同步修正摘要；不把新流控或 Cookie 协议反向套用到旧 Grok 通道。

## 进入编码前的证据缺口

1. 固定 opencode 后端及官方前端版本，核对前端构建/取得方式和许可文件。
2. 确认该版本官方前端能在 hostOrigin 下同源访问后端；资源不能依赖一个未配置的远端服务。
3. 确认官方前端能在同站点不同 origin 的 iframe 中运行，核对 CSP、X-Frame-Options、跳转、下载、剪贴板等能力。不能用扩大权限或移除未知安全策略代替兼容性确认。
4. 确认 opencode 对公开 Origin、反代 Host、WS Origin 的处理；决定它自身是否需要允许 hostOrigin 的 `--cors` 配置。同源浏览器请求无需 shell→host 的 CORS 通道。
5. 核对 capri-hub、capri-fe 的源码/仓库可用性及旧 Grok 视图入口；当前目录已有 capri-host，其他组件的修改需要对应源码。

这些是未来实现阶段的检查任务，本轮没有启动服务、运行测试或修改代码。
