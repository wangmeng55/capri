# capri-hub · 公网入口、认证与隧道桥接设计

> 设计修订稿；具体契约见[C1](03-contract-origin-routing.md)、[C2](03-contract-browser-auth.md)、[C3](03-contract-http-tunnel.md)、[C4](03-contract-stream-lifecycle.md)。对应hub源码尚待取得/核对。

## 1. 部署与责任

首版在一台公网服务器/VPS上运行一个capri-hub实例。可以由前置HTTPS反代终止TLS，也可以由hub直接终止；前置反代必须保留有效authority、支持SSE不缓冲和WS升级，并配置适合长连接的期限。hub只信任明确配置的入口反代提供的Forwarded头，不能信任公网客户端任意填写的同名头。

hub托管浏览器TS外壳，管理登录/票据与host登记；按主机子域把请求桥接到已认证host的出站隧道。它不启动被控机器的agent，不操作项目文件，不持有opencode本机Basic。

## 2. 组件分工

| 组件 | 状态/操作 |
|---|---|
| HostRegistry | 持久hostId/kind/originLabel，内存online/ready/当前控制与数据会话、能力 |
| OriginRouter | 校验authority，查hostId，拒绝任意目标URL或未知子域 |
| BrowserAuth | shell/view会话、绝对期限、CSRF nonce、登录/撤销 |
| TicketStore | 60秒view-ticket与30秒tunnelTicket，绑定身份、原子一次性消费 |
| RelayServer | 接收出站控制/数据连接，能力协商、connId绑定、上下线通知 |
| HTTPGateway | 浏览器请求→C3帧；response-start→状态和头；逐块body写回及flush |
| UpgradeGateway | 校验浏览器WS身份/来源，透传握手，合法101后双向字节桥接 |
| TunnelRegistry | connId/reqId到浏览器context/认证会话的关联、配额与清理 |
| LegacyGrokAdapter | 继续原request/respond、事件seq/replay与现有FE入口 |

这是责任设计，不假定hub已经具备这些组件；实际目录结构在取得源码后确定。

## 3. host登记与数据通道

host配对身份决定它可登记的hostId；hub不可让另一个token覆盖同一host。显示名和kind由受认证host登记，kind变化需按管理策略重新确认，不能悄悄把既有Grok记录当作opencode路由。

control连接建立与后端ready是独立状态。新透明能力缺失、data连接尚未建立、后端未就绪时，opencode视图请求返回503。WS数据连接绑定有效control会话；control失效即关闭附属数据连接。重连替换旧连接时，为旧connId批量取消所有请求，再启用新连接，不搬迁旧reqId。

单机hub首版的会话/票据可使用内存存储，host登记/originLabel需持久保存。hub进程重启后浏览器重新登录，host重新连接，所有原网络流失败结束；不承诺多实例共享会话或无缝高可用。

## 4. 浏览器入口

- shellOrigin上的静态外壳可提供登录页；管理API要求有效shell会话。保持旧Grok入口既有认证兼容逻辑，新增会话认证不能未经回归直接移除旧客户端支持。
- hostOrigin上的/__capri/session由hub消费view票据；它不进入透明隧道。其余opencode页面、资源和业务请求在view认证后进入隧道。
- HTTPGateway创建浏览器context，并在取消/期限到达时按C4发cancel。会话记录反向关联到活跃reqId，logout能立即关掉SSE/WS。
- 普通HTTP在response-start前不写最终状态；收到数据即时写回，SSE及时flush。前置反代必须同样不缓冲；不能仅把host Flush改好就认为全链路流式已成立。
- 已提交响应后发生stream-error要异常结束本条HTTP流，不能把残缺数据以正常EOF返回。
- UpgradeGateway保留浏览器key，校验本机101和accept后写回浏览器；拒绝走实际HTTP响应，成功则切换raw字节流。

## 5. 配额、诊断与隔离

限制来源会话、host和hub的活跃流/缓冲预算，默认参数按C4。日志只记录必要的hostId、connId、reqId、状态、时长、字节量和固定错误类别；不记录票据/Cookie/Authorization或body。包含敏感业务query的完整URL也不写默认日志。

区分主机离线、后端未就绪、认证失效、能力缺失、本机拒绝和代理失败。慢流只影响自己的lane；WS透明数据与旧Grok事件writer隔离。底层连接完全断开仍会影响其全部透明流，不能宣称只影响一条请求。

## 6. 未来验收

DNS/TLS与authority隔离、票据重复消费、登录退出、同站点兄弟子域Origin、并发流及预算、SSE全链路flush、握手拒绝、旧connId清理、hub重启、旧Grok认证/配对/事件回归。当前未实施或运行这些验证。
