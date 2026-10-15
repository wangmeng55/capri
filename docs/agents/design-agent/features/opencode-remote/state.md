# Session State · opencode-remote

## 当前阶段

- 日期：2026-10-14
- 阶段：设计修订稿已完成，供用户审阅；尚未编码
- 本轮授权：用户要求把四份契约写清楚，并把交互链路纳入设计；明确禁止实现代码
- 本轮范围：Markdown设计文档修订、引用与一致性检查
- 文档检查：13份Markdown的本地链接与代码围栏完整；交互文件含12张Mermaid图。未进行实际Mermaid渲染或软件运行验证。
- 已实现功能/软件测试：无
- 架构方向：共享传输库抽取、双host二进制、按kind分叉、自建透明HTTP平面、复用官方opencode前端

## 本轮产物

| 产物 | 内容 |
|---|---|
| [总体设计](01-overview.md) | hub部署、职责与数据边界、实施阶段、风险及修订后的模块分工 |
| [契约目录](03-contracts/README.md) | C1–C4索引、设计权威关系、版本与兼容性前置检查 |
| [C1](03-contracts/origin-routing.md) | 多host子域到同一hub、官方iframe、HTTP头规则与直连 |
| [C2](03-contracts/browser-auth.md) | shell/view会话、一次性票据、HTTP/SSE/WS认证与来源检查 |
| [C3](03-contracts/http-tunnel.md) | 能力协商、独立数据通道、帧序列、升级字节和窗口 |
| [C4](03-contracts/stream-lifecycle.md) | 取消、背压、预算、期限、重连与Supervisor |
| [时序图](04-interactions.md) | 12个部署/交互场景及未来实施顺序 |
| [模块设计](02-modules/capri-hub.md) | 已同步shared-transport/opencode-host/shell，并补充hub设计 |

## 当前方案与尚未验证项

本轮具体设计：HTTPS主机子域 + 同源官方iframe、host-only Cookie、POST一次性票据、JSON/base64隧道块、WS握手透传与升级raw字节、有界窗口、断线不自动重放。它们是设计方案，不标记为用户逐项确认或已通过实测。

尚需取得证据：固定官方前后端版本与资源、iframe/CSP与手机体验、真实hostOrigin/Host/WS Origin处理、供应商配置、SSE断线重建与状态恢复、capri-hub/capri-fe源码及Grok视图入口。具体默认参数尚未负载验证。

需求原文和旧state可能仍出现“单进程”“shellOrigin CORS”“透明平面Basic注入”“保留Authorization”等旧技术措辞。本轮在设计中修正为单启动入口/公开端口、iframe业务同源、LocalProxy注入本机Basic；没有擅自把需求原始记录覆盖成已确认的新结论。

## 历史探索记录

此前M0记录：opencode 1.18.25，HTTP/OpenAPI 162端点，SSE事件、PTY WS、Basic认证。capri-host原分析指出hub依赖Grok具体类型、relay Flush no-op、respond单终帧；这些是历史本机能力记录，不是公网端到端结果。

原state记录过perform-automated-review=yes、max-automated-review-iterations=3；正式自动设计评审仍未执行。本轮文档一致性检查不计为正式设计评审或软件验收。

## 后续实施顺序

设计/版本前置检查 → M1共享库与Grok回归 → M2a普通API → M2b SSE → M2c WS → M3a远程审批 → M3b主机切换 → M4直连/打包。

目前停留在设计阶段。下一步的版本验证或编码需要来自用户的新指令；本轮没有启动服务、编写实现或运行软件测试。
