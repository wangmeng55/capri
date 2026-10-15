# C1 · host origin 映射到 hub

## 1. 部署与域名

首版使用一台公网服务器运行一个 capri-hub 实例。服务器可以是用户自己的 VPS；hub 是其中运行的服务进程。它托管外壳、验证浏览器身份、登记 host，并转发请求。命令、模型调用、文件操作与会话存储发生在被控机器。

以下域名是示例，部署时替换为用户自己的域名：

| 地址 | DNS 指向 | 内容/职责 |
|---|---|---|
| `https://console.example.com` | hub 公网地址 | 外壳、登录、host 列表和票据 API |
| `https://h-a1.hosts.example.com` | 同一个 hub 公网地址 | opencode 主机 a1 的前端、HTTP/SSE/WS |
| `https://h-b2.hosts.example.com` | 同一个 hub 公网地址 | opencode 主机 b2 的前端、HTTP/SSE/WS |
| `http://127.0.0.1:<privatePort>` | 被控机器本机 | opencode serve 私有后端，仅 host 访问 |

公网 DNS 配置 `console.example.com` 与 `*.hosts.example.com`；HTTPS 证书覆盖这两种名称。一个通配证书不自动覆盖跨层名称，证书和 DNS 都应逐项核对。所有浏览器公网入口使用 HTTPS/WSS 和端口 443。host 的 QUIC/WS 出站地址复用现有 hub 配置，服务端口按实际部署开放；不硬编码示例端口到新协议。

多个 host origin 可以落在同一台服务器、同一个监听端口。独立 origin 不意味着每台 host 需要公网 IP 或额外 VPS。

## 2. hub 路由表

hub 在成功认证 host 后持有：

| 字段 | 含义 |
|---|---|
| hostId | 稳定、不可被显示名称替代的主机身份 |
| kind | grok / opencode |
| originLabel | hub 分配的稳定 DNS label，例如 h-a1 |
| origin | 由部署域名配置和 originLabel 构造的 HTTPS origin |
| online / ready | 隧道是否在线、本机后端是否就绪 |
| connId | 当前认证后的 host 连接身份 |
| capabilities | 当前连接协商的协议能力 |

originLabel 使用 hub 分配的随机、合法 DNS label，不直接使用主机名或未经校验的 hostId。host 不得提交任意 origin 要求 hub 为其代理；浏览器也不能通过 query 指定任意本机目标 URL。

每条 HTTP 请求按顺序执行：

1. 校验 TLS 对应的部署入口和 HTTP authority/Host，仅允许配置的外壳名称和注册的 host 名称；可信入口反代提供的 Host 也必须校验。未知 authority 返回 421，不进入隧道。
2. 从 authority 查找 hostId；对缺失或已删除登记返回 404。主机名重命名不会改变 originLabel。
3. 验证绑定到该 hostId 的浏览器视图会话，检查来源策略。
4. 检查主机在线、ready、能力和并发配额；离线/未就绪/能力不足返回 503，配额已满返回 429。
5. 绑定当前 connId，创建 reqId，通过 C3 隧道下发 origin-form 路径和原始 query。

不可接受 `CONNECT` 或 absolute-form URL 把代理变为任意目的地隧道。目标始终是该 host 本机配置的 opencode 服务。

## 3. 官方前端承载与外壳

外壳常驻 `console.example.com`。选择 opencode 主机后，外壳用 iframe 展示该 hostOrigin 的 `/`。iframe 内的官方前端使用自身 hostOrigin 发起 HTTP/SSE/WS，前端、API 和 WS 都同源；父页面不会读取 iframe DOM 或改写 opencode API。

hostOrigin 上除了保留的 `/__capri/` 网关路径，其他路径都透明转发给 host 本机 opencode serve，包括 `/`、JS/CSS 资源、SPA 深链、API 和 WS 端点。优先使用已确认版本 server 提供的官方前端；若需要本地封装官方构建产物，由 host 的视图适配器提供同一套未经业务修改的产物，并固定其版本。前端获取方式必须验证后记录，不能把旧 M0 的 `/` 返回 HTML 当成全部资源已可离线提供的证明。

hub 为 iframe 响应设置/核对 `Content-Security-Policy: frame-ancestors https://console.example.com`；若上游 CSP/X-Frame-Options 禁止这种嵌入，视为兼容性阻塞，先研究官方支持的承载方式，不盲目删除策略。外壳自身的 frame-src 仅允许已配置 host 子域。首版不以 `sandbox` 的 opaque origin 模式加载官方应用。

Grok 视图走现有 capri 前端与旧中继适配器，保持现有 API/事件契约；只有外壳选择层认识 kind。具体 Grok 前端入口需从 capri-fe/capri-hub 源码核对，不能为了统一 iframe 拓扑把旧 Grok 请求改成透明隧道。

## 4. HTTP 元数据的代理规则

透明指不理解 agent 业务端点；标准代理仍须处理连接级元数据。

- target 保留 escaped path 和 raw query，包括重复 query 参数；禁止回退为任意本机地址。固定目标 baseURL 由 host 配置，不由请求决定。
- headers 使用有序的名称/值数组，保留重复头，尤其 Set-Cookie；不能使用会覆盖重复值的 Map<string,string>。
- 普通 HTTP 剥离 Connection 及其列出的逐跳头、Keep-Alive、Proxy-Authenticate、Proxy-Authorization、Transfer-Encoding 等；传输分块由每个 HTTP 连接自行处理。声明的 trailer 名称和值按 C3 传递。WS 的 Upgrade/Connection 只在专用升级分支按 C3 使用。
- host LocalProxy 把 HTTP Host 设为固定 loopback 目标；保留真实浏览器 Origin/Referer，不伪造成 localhost 绕过来源校验。它可为该 opencode 版本配置允许的 hostOrigin。
- hub 删除浏览器伪造的 Forwarded/X-Forwarded-*，按部署信任边界重新生成；host 只信任已认证 hub 提供的 publicOrigin 元数据。
- host 从请求删除网关会话 Cookie 和浏览器入站 Authorization，再按本机配置注入 opencode Basic；其他应用头按受支持 HTTP 语义透传。凭证不进入隧道响应和普通日志。
- 保持 Content-Encoding 与字节体一致；SSE 不额外压缩/缓冲，host 对上游使用 identity 优先策略，禁止自动解压后仍转发原长度/编码头。
- 本机基地址的同源绝对 Location 可以由视图适配器按已校验 publicOrigin 改为公开同源地址；其他 Location 原样保留。若官方前端依赖本机绝对 URL/Cookie Domain 等，列为兼容性问题，不引入端点级改写。
- 对 opencodeOrigin 的 Cookie 不允许设置覆盖父域的 Domain；首版拒绝这类上游 Set-Cookie 并报告兼容性问题。__Host-capri_* 名称保留给网关，上游不能覆盖。

## 5. 本机直连与进程/端口

opencode-host 是一个用户启动入口，实际进程至少包括 host 与它监管的 opencode serve 子进程。原先“单进程”描述修正为“单启动入口、一个对用户公开的 HTTP 端口”；私有后端端口不计为公开入口。

若实现本机/局域网直连，opencode-host 还须提供可选 LocalGateway：同端口提供官方前端、认证入口和反向代理，复用 LocalProxy；绕过 hub 和隧道。默认仅绑定 loopback；开放到 LAN 时必须配置直连门禁，否则启动失败。公开监听端口与 privatePort 分开。

直连页面用它自己的同源会话认证：loopback HTTP 使用 HttpOnly、SameSite=Strict 的普通 host-only Cookie（不使用需要 Secure 的 __Host- 前缀），只接受精确本机 Origin。LAN 直连要求 HTTPS，按 C2 的 Secure Cookie 和精确 Origin 规则。公网 shell 到 LAN 的自动探测、混合内容和私网访问权限不在首个远程链路里解决；用户可直接打开直连地址，hub 离线不影响该路径。

## 6. 待实现验收

- 两个 hostOrigin 同时映射到同一个 hub，任一请求都不能串到另一台 host。
- 每个 origin 的 `/`、资源、API、SSE、WS 和 SPA 深链完整可用。
- 伪造 Host、absolute URL、删除后的 hostId、跨主机视图 Cookie 都无法穿过路由检查。
- iframe 内官方前端正常运行，主机切换不依赖修改其业务代码。
- DNS/TLS、版本及 iframe 兼容性是部署/实现前置条件，当前尚未验证。
