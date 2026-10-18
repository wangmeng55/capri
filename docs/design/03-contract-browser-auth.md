# C2 · 浏览器认证覆盖 HTTP / SSE / WebSocket

## 1. 三种凭证

| 凭证 | 持有方 | 校验处 | 用途 |
|---|---|---|---|
| FE_TOKEN 或登录密码 | 用户；提交给 shell 登录端点 | hub | 建立浏览器会话 |
| host 配对 token | host 本机 | hub host 连接入口 | 认证出站隧道与 hostId |
| opencode Basic 凭证 | opencode-host 与本机 server | opencode serve | host 调本机 API |

三者互不替代。FE_TOKEN 不注入 opencode；opencode 密码不交给浏览器；浏览器 Cookie 不充当 host 配对凭证。共享传输库提供通用元数据接口，Basic 的构造和配置由 opencode-host 的 LocalProxy 负责。

## 2. 登录与会话

首版公网浏览器认证使用 hub 管理的不透明会话 Cookie。原生 EventSource 和 WebSocket 的构造参数不支持任意自定义认证头，因此不能把所有前端连接设计为必须携带 FE_TOKEN 请求头；同源 Cookie 能覆盖普通 fetch、EventSource/流式 fetch 与 WS 握手。

| Cookie | 作用域 | 属性 | 会话约束 |
|---|---|---|---|
| `__Host-capri_shell` | console.example.com | Secure; HttpOnly; Path=/; SameSite=Lax；不设置 Domain | 绑定 shellSessionId；绝对期限默认 8 小时 |
| `__Host-capri_view` | 某台 hostOrigin | Secure; HttpOnly; Path=/; SameSite=Lax；不设置 Domain | 绑定 shellSessionId + hostId；期限不晚于父会话 |

shell 与 hostOrigin 都使用 HTTPS，并位于同一个已注册域名下，是跨 origin、同 site。独立 host-only Cookie 不发给其他主机子域；不依赖跨 site 第三方 Cookie。Cookie 安全属性、同 site iframe 行为需要在目标手机浏览器验证。

所有 session id 为高熵随机值，hub 保存其哈希/会话记录并可即时撤销；不把长期 FE_TOKEN 写入 Cookie、URL、localStorage。登录请求和响应不缓存、不记录凭证。

## 3. API 与跨 origin 票据交换

下列是新设计 API，实施时还需核对旧 hub 路由是否冲突；它们属于 hub 控制面，不进入 opencode 隧道。

| 方法/路径 | origin | 请求 | 成功结果 |
|---|---|---|---|
| POST `/api/auth/login` | shell | JSON FE_TOKEN/密码；精确 shell Origin；application/json | 建立 shell Cookie，返回 expiresAt、CSRF nonce |
| GET `/api/auth/session` | shell | shell Cookie | 返回当前状态、expiresAt、CSRF nonce，不返回 token |
| POST `/api/auth/logout` | shell | shell Cookie + X-Capri-CSRF | 撤销父会话及所有视图会话，清 shell Cookie |
| GET `/api/hosts` | shell | shell Cookie | 已认证 host 列表，含 kind、origin、online、ready、capabilities |
| POST `/api/hosts/{hostId}/view-ticket` | shell | shell Cookie + X-Capri-CSRF | `{origin, ticket, expiresAt}`，Cache-Control: no-store |
| POST `/__capri/session` | hostOrigin | form-urlencoded 票据；Origin 必须为 shellOrigin | 原子消费票据，设 view Cookie，303 到固定 `/` |

登录端点只接受 JSON 和精确 shell Origin，限制失败速率，避免跨站表单登录。其余 shell 写操作要求随机 CSRF nonce 与精确 Origin；nonce 绑定 shell 会话。

票据至少 256 bit 随机，默认有效 60 秒，一次性，hub 记录票据哈希、hostId、父会话和期限。兑换时重新检查父会话及目标 hostOrigin，原子消费，不接受自定义 redirect URL。不能把票据放 query 或 iframe src。

外壳获取票据后，通过目标为新 iframe 的 POST 表单提交到 `https://h-a1.hosts.example.com/__capri/session`。该端点由 hub 处理；它用票据识别父会话，无需读取 shell 的 host-only Cookie，也无需开放 CORS。303 后 iframe 请求 `/`，携带新 view Cookie。票据暂存于 shell 内存，提交后丢弃。

## 4. opencode 视图的认证规则

iframe 内运行官方前端，使用自身 origin 的 API 和 wss 地址。hub 在每条请求/升级前验证 view Cookie、父会话和 hostId。

| 请求类型 | 携带身份 | 来源策略 | 认证失败 |
|---|---|---|---|
| 页面/静态资源 GET | view Cookie | iframe 初始导航允许 shell；资源同源 | HTML 导航返回 hub 的认证过期页面 |
| 普通 API GET/HEAD | view Cookie | 若有 Origin 必须为 hostOrigin；跨源 CORS 不开放 | 401，不把登录页当成功 API 响应 |
| API 非安全方法 | view Cookie | Origin 必须精确 hostOrigin；确实无 Origin 时仅接受同源 Referer，二者均无则拒绝 | 401/403 |
| SSE GET / 流式 fetch | view Cookie | 同源；有 Origin 时精确匹配 | 开始响应前 401/403 |
| WS Upgrade | view Cookie | Origin 必须精确 hostOrigin；查询票据只是 opencode PTY 附加认证，不能替代 view Cookie | 升级前 401/403，不返回 101 |

GET/HEAD 等安全方法不得用于新网关的状态修改。opencode 既有接口是否满足安全方法语义，需要版本兼容检查。兄弟子域是同 site，SameSite 本身不足以阻止兄弟子域发起请求，因此必须保留精确 Origin/Referer 检查。官方前端不需要知道网关 CSRF token；其同源写请求来源是网关的检查依据。

hub 对 opencode 视图的 CORS 不开放 shell 跨源业务调用；无需为 shell 注入统一 SDK。WS 没有普通 fetch 式的 CORS preflight，必须在握手中直接校验 Cookie 和 Origin。前端向后端传的业务 token/query 原样转发，不计入 capri 的登录机制。

## 5. 登录过期、退出与主机切换

- 会话有绝对期限。hub 为已打开 SSE/WS 设置相同的撤销/到期计时器；到期主动关闭隧道，不能仅在初次握手时检查。
- logout 撤销父会话，进而撤销所有 host view 会话，取消这些会话关联的请求/隧道。其他 origin 的 Cookie 可能仍存在浏览器里，但服务端已失效，后续兑换会覆盖它们。
- shell 定期检查父会话状态（默认 60 秒）、主机状态，并为重新选择的视图重新兑换票据。它不能跨 origin 读取 opencode iframe 的 DOM/响应错误；界面不得假设能从 iframe 内部获得未约定的 postMessage。
- 切换主机先卸载旧 iframe，旧页面的请求/SSE/WS 随页面取消。新 iframe 绑定新的 hostId 会话；切换不调用 opencode 的会话删除/任务中止接口。
- 关闭页面或撤销 capri 会话只取消网络请求/终端连接。agent 回合是否继续由 opencode 语义决定；不能承诺取消连接必定保持任务，也不能把它自动解释为停止 agent。

## 6. 本机凭证注入

opencode-host 为其监管的 server 配置本机 Basic 凭证，健康检查、HTTP 与 WS 握手都使用同一配置。LocalProxy 删除入站 Authorization 和网关 Cookie，再注入本机 Basic；共享传输库不读取 OPENCODE_SERVER_PASSWORD、不包含 opencode 名称或端点逻辑。

凭证未正确配置或校验失败时，本机服务返回的 401 按普通响应传回，并在 host 诊断中标记内部凭证问题。该错误不能触发 hub 自动把 FE_TOKEN 当作 opencode 密码重试。

## 7. 未来验收与依据

未来验证：普通 API/SSE/WS 均只依赖各自 origin Cookie；跨 hostId Cookie、重复/过期票据、错误 Origin、退出后活跃 WS/SSE 都被拒绝或关闭。iframe 的 Cookie 与官方应用兼容性目前未测试。

规范依据：[EventSource 接口](https://html.spec.whatwg.org/multipage/server-sent-events.html#the-eventsource-interface)、[WebSockets Standard](https://websockets.spec.whatwg.org/)、[Set-Cookie 属性和前缀](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Set-Cookie)。这些约束支持 Cookie 同源认证方案，不构成对当前 opencode 前端实现方式的确认。
