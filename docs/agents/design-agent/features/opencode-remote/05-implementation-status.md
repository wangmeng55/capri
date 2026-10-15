# opencode-remote · 实施状态（M1–M3a 已落地）

> 日期：2026-10-14。本文件记录**实际写下的代码与验证结果**，严格区分
> 「设计选择 / 已实现 / 已验证 / 待验证」。设计文档（01–04）仍是目标与契约来源。

## 0. 工作区与工具链（本次实际建立）

| 项 | 位置 | 说明 |
|---|---|---|
| capri-host | `D:\1_2027_AI\opencode\capri-host` | 已有，改造：抽库、加 opencode-host |
| capri-hub | `D:\1_2027_AI\opencode\capri-hub` | 按 README 记录的 `github.com/AgentsHarness/capri-hub` 克隆（HEAD `fc3fd71`），仅构建验证 |
| capri-fe | `D:\1_2027_AI\opencode\capri-fe` | 按 README 记录的 `github.com/AgentsHarness/capri-fe` 克隆（HEAD `734fb12f`） |
| capri-transport | `D:\1_2027_AI\opencode\capri-transport` | **新增独立 Go module**（模块名 provisional，见 §5） |
| go.work | `D:\1_2027_AI\opencode\go.work` | 联合 capri-host / capri-transport / capri-hub |
| Go 工具链 | `tools\go`（go1.27.2） | 依赖与构建缓存按用户要求放在 `capri-host\.gopath`、`capri-host\.gocache` |
| opencode | `D:\opencode\bin\opencode.exe` | **实测版本 1.18.25**（与历史 M0 一致，本次重新核实） |

`.gopath`/`.gocache` 已加入 `capri-host\.git/info/exclude`（本地，不污染仓库）。

## 1. D0 前置检查（本次实测证据）

对真实 `opencode serve`（1.18.25，loopback）探测：

| # | 结论 | 证据 |
|---|---|---|
| D0-1 | 版本 = **1.18.25** | `opencode --version` |
| D0-2 | `/global/health` → `200 {"healthy":true,"version":"1.18.25"}` | curl |
| D0-3 | **官方前端由 serve 自身提供**：`GET /` 返回 2884B HTML，引用 `/assets/index-*.js`+`.css`，含 `#root` | curl |
| D0-4 | 前端 API base = **`location.origin`**（仅 host 为 `opencode.ai` 时回退 `localhost:4096`） | JS bundle 反查 `location.origin` |
| D0-5 | 官方页面 CSP：`default-src 'self'; script-src 'self' 'wasm-unsafe-eval' …; connect-src * …`；**无 `frame-ancestors`、无 `X-Frame-Options`** → iframe 同站点嵌入不被应用自身阻止 | curl -D |
| D0-6 | 鉴权 = HTTP Basic：无凭证 `401 + WWW-Authenticate: Basic realm="Secure Area"`；带声明凭证 200 | curl |
| D0-7 | `--cors <origin>` 生效：允许源的预检 `204 + Access-Control-Allow-Origin`，非允许源不返回 ACAO | curl |
| D0-8 | SSE `/event` = `text/event-stream`，首帧 `server.connected` | curl -N |
| D0-9 | 未知路径返回 SPA（`/assets/` → 200 text/html）→ **URL 路径前缀承载不可行**，印证独立 origin 选择 | curl |
| D0-10 | 资源路径为绝对 `/assets/...` → 同源（hostOrigin）加载即可，无需 base path | HTML |

**未验证（仍是前置缺口）**：真实手机 + 公网、同站点不同 origin 的 iframe 真机行为、
官方前端在 hostOrigin（子域）下的完整交互。D0-5 说明「无 frame-ancestors」，
但最终仍需真机确认。

## 2. M1 共享传输库（已实现 / 已验证）

**已实现**
- 新模块 `github.com/AgentsHarness/capri-transport`，公开包：`transport/`、`hubstate/`、`netinfo/`、`procattr/`。
- `hubstate` 新增路径助手 `Dir/Path/HubStatePath`；capri-host `internal/config` 改为薄封装委托。
- 原 `capri-host/internal/hub`（client/control/manager）整体迁入 `transport`，**去掉 `internal/acp` 与 `internal/config` 依赖**。
- 中立接口 `transport.EventSource`（`Subscribe() (chan Event, func())` / `Snapshot() Status`）、`transport.Status`、`transport.Event = map[string]any`（别名）、可选 `NameSetter`。
- capri-host 适配器 `internal/hubadapt.ACPBridge` 把 `*acp.Bridge` 接入中立接口；`acp.Event` 改为类型别名 `map[string]any`（无方法，安全）。
- 旧 Grok 语义（seq 分配、replay、respond、鉴权、压缩）**逻辑未改，仅类型改名/换 import**。

**已验证**
- `capri-transport`：`go build ./...` + `go test ./...` 全通过（含迁入的 client/manager/control/dualstream/migration/quicpin/uplink 等测试）。
- `capri-host`：`go build ./...` 通过；`go test ./...` 失败集合与改造前**完全一致**（`internal/acp` 3 项、`internal/config` 2 项、`internal/server` 既有列表），无新增失败 → Grok 无回归。
- 迁出的两个宿主耦合测试迁到 `internal/hubadapt`：真实 server 链的 in-process vs loopback 等价测试**通过**；`e2e` tag 编译通过。

## 3. M2 透明隧道协议（已实现 / 已用模拟本机服务验证）

**已实现**（`capri-transport/transport`）
- `tunnel_frame.go`：`http-tunnel-v1` 帧编解码、op/lane 常量、多值头（保序、Set-Cookie 重复保留）、限额（decoded≤64KiB、wire≤256KiB、header≤64KiB/200 值、CR/LF/NUL 拒绝）。
- `tunnel_flow.go`：每 lane 独立发送窗口（`flowWindow`）+ 接收端 ack 合并（64KiB 或 20ms 阈值）。
- `tunnel_req.go` / `tunnel_host.go`：**host 侧执行器** `HostTunnel`：http-open→流式调用 `LocalTarget.ForwardHTTP`，response-start/data/end 逐块回传；request-data/end 上传；request-stop/cancel/window-update；**WS 握手透传 + 升级后原始字节双向转发**（预读缓冲保留）。
- `tunnel_hub.go`：**hub 侧网关** `TunnelClient`（reqId 解复用、SSE 即时 Write+Flush、窗口更新、取消传播、WS 升级桥接）。
- `relaytest.go`：`Client.RelayForTest`（供跨模块集成测试用的最小导出 seam）。

**已验证**（`tunnel_e2e_test.go`，模拟本机 HTTP/SSE/WS 上游 + 内存帧连接）
- 普通 GET `/global/health` → 200 + 原 JSON。
- 多值响应头（Set-Cookie a=1/b=2）在隧道后仍为两条。
- SSE：三事件经 **≥3 个 response-data 帧**增量传输（断言未被合成单帧），正文正确。
- WS：`ws-open`→`ws-ready(101)`；ws-up 字节到达上游、ws-down 字节回到浏览器。
- 帧编解码往返、超限 wire 帧拒绝、header 值数上限、CRLF 注入拒绝。

**待验证**：QUIC 独立 stream 形态、真实反代不缓冲、慢客户端背压、认证到期清流、断线墓碑迟到帧（有实现，未在真链路验证）。

## 4. M3a opencode host 核心（已实现 / 已对真实 opencode 验证）

**已实现**（`capri-host/internal/ochost` + `cmd/opencode-host`）
- `discover.go`：`DiscoverOpencodeBin`（显式路径 → `OPENCODE_BIN` → PATH → 常见目录）。
- `supervisor.go`：spawn `opencode serve --port <private> --hostname 127.0.0.1`，带 `OPENCODE_SERVER_PASSWORD`，健康探测 `/global/health`（携带 Basic），30–45s 就绪期限，失败即回收子进程；`Stop` 终止回收；`Ready/LastError/BaseURL`。
- `localproxy.go`：`LocalProxy` 实现 `transport.LocalTarget`：删除入站 Authorization/Cookie，**注入本机 Basic**；`ForwardHTTP` 流式转发（SSE 不缓冲）；`ForwardUpgrade` 原始 Upgrade 透传（保留 `Sec-WebSocket-Key`，含预读缓冲）；`ServeUpgrade` 供 LocalGateway 直连。
- `cmd/opencode-host/main.go`：**LocalGateway**（直连交付项）：非回环绑定无 `OCHOST_TOKEN` 则 fail-fast；serve 官方前端 + HTTP/SSE/WS 代理（HTTP 走 `ForwardHTTP`，Upgrade 走 `ServeUpgrade`）。

**已验证**
- `internal/ochost` 集成测试启动**真实 opencode 1.18.25**，经「HostTunnel → LocalProxy → loopback opencode」得到 `{"healthy":true,"version":"1.18.25"}`（Basic 注入生效）。
- 实跑 `opencode-host.exe`：`GET /global/health` 200；`GET /` 返回官方前端 2884B HTML。
- 失败/回收路径：无 opencode 时启动报错（可复现）。

**待验证**：进程树回收（Windows Job Object 完整方案）、退避重启预算、端口冲突判定、真实 PTY WS 经隧道、移动端体验。

## 5. 与原设计的差异 / 需人工确认

1. **共享 module 名称 provisional**：`github.com/AgentsHarness/capri-transport` 是按仓库命名惯例取的**本地临时名**，未创建、未推送任何远程仓库。最终发布名与位置需用户确认；改名只需改 `go.mod` 模块路径与各 `replace`。
2. **`Config.Local` 与 `NameSetter`**：为去除 config/acp 依赖，`Rename` 的本机改名落盘改为注入回调 `persistName`；显示名同步到事件源改为可选接口 `NameSetter`。语义不变。
3. **测试迁移与 seam**：`relay_server_chain_test.go`/`e2e_hub_binary_test.go` 从共享模块迁回 `capri-host/internal/hubadapt`；新增导出 `Client.RelayForTest` 作为唯一的跨模块测试 seam（非生产路径）。

## 4b. M3b hub 控制面（已实现 / 已单测）

**已实现**（`capri-hub/internal/transparent`，新包；并在 `cmd/capri-hub/main.go` 的 `buildHandler` 中挂载路由）
- **origin 路由** `Router`：hostId → 稳定 label（`h-<sha256 前10>`）→ `https://<label>.<hosts-domain>`；`LabelForAuthority` 只接受配置的 hosts 域（拒绝裸域/多层/任意域）；`IsShellHost`。
- **浏览器会话** `SessionStore`：`__Host-capri_shell`（8h 绝对期限、HttpOnly/Secure/SameSite=Lax、无 Domain），高熵 id + CSRF nonce；`Get/Revoke`。
- **一次性 view 票据** `TicketStore`：256bit 随机、60s、**哈希存储、原子一次性消费**。
- **host-only 视图会话**：`NewView(shellID,hostID)`，期限不超过父会话；`ValidView(viewID,hostID)` 校验宿主绑定 + 父会话存活。
- **控制端点**：`POST /api/auth/login`（JSON+精确 Origin+常量时间比较）、`GET /api/auth/session`、`POST /api/auth/logout`（需 CSRF）、`POST /api/hosts/{hostId}/view-ticket`（需 CSRF、校验 host 存在）、`POST /__capri/session`（Origin 校验 + 一次性票据 + 权威/宿主一致性校验 + 设 view Cookie + **303 → /**）。

**已验证**（`internal/transparent` 单测，全通过）
- 权威路由：label 稳定、合法子域可解析、`evil.example.com`/裸域/`a.b.hosts...` 拒绝。
- 票据：一次性（二次消费失败）、过期失败。
- 视图会话：跨 host 无效、父会话撤销后立即失效。
- HTTP：错误 Origin→403、错误 token→401、登录/会话/登出（无 CSRF→403）、view-ticket（未知 host→404）、`/__capri/session` 成功 303 且设 view Cookie、**票据二次兑换→403**、h2 独立兑换成功。
- `capri-hub`：`go build ./...` 通过；`cmd/capri-hub`、`internal/transparent` 测试通过；`internal/hub` 仅 `TestPersistFileMode` 失败（Windows 不强制 POSIX `0600` 位，属既有环境性失败，非本次改动）。

**未验证**：真实 DNS/TLS/子域证书、真机 Cookie/iframe。

## 4c. M3b 数据面 + host 远程接入（已实现 / 已验证）

**已实现**
- 共享库（`capri-transport/transport`）：`WSFrameConn`（一帧=一条 WS text 消息）、`TunnelHostAgent`（FrameConn→HostTunnel 运行环）。
- capri-hub（`internal/transparent/datplane.go`）：`TunnelTicketStore`（30s 一次性）、`DataPlane`：
  - `GET /ws/host/tunnel`：host 以 `Authorization: Bearer <ticket>` 接入 → 分配 connId → 建 `TunnelClient` 并登记到 hostId；
  - `ServeHost/Dispatch`：按 authority → label → hostId，校验 **host-only view Cookie** 与精确 Origin，转发到该 host 的 `TunnelClient`（HTTP/SSE/WS）；
  - 未知 host origin→404、无会话→401（HTML 导航返回过期页）、宿主未就绪→503、非 host authority 回落 shell。
- capri-hub `buildHandler`：挂载 `GET /ws/host/tunnel`、`POST /api/hosts/tunnel-ticket`（**由 host 配对 token 鉴权**，非浏览器管理面），并把服务器 handler 改为 `gateway.Plane().Dispatch(mux)`。
- capri-host `internal/ochost/hubconn.go`：`RunHubDataConnection`（取票据 → 拨 `/ws/host/tunnel` → 跑 host 平面，带退避重连），在 `cmd/opencode-host` 中由 `OCHOST_HUB_URL` 启用（LocalGateway 并行保留，hub 挂掉仍可直连）。

**已验证**
- `capri-transport` `TestTunnelOverRealWebSocket`：真实 TCP/WS 上「浏览器→mock hub→WS 隧道→host→mock 上游」得到 200 + 原 JSON。
- `capri-hub` `TestDataPlaneHostOriginRoundTrip`：真实 WS 上注册 host 数据连接后，浏览器请求经 hostOrigin 校验 view Cookie 到达 host；无 Cookie→401；非 host authority 回落 shell。
- 全模块 `go build`/`go test`：capri-transport 全过；capri-hub `cmd`+`transparent` 全过（`internal/hub` 仅既有 `TestPersistFileMode` Windows 失败）；capri-host 新包全过（`acp/config/server` 为既有环境性失败）。

**未验证**：真实 DNS/TLS/子域证书、真机 iframe、QUIC 数据连接形态（当前走 WS 降级通道）。

## 4d. 浏览器外壳 + 全链路 e2e + 部署（已实现 / 已验证）

**已实现**
- 外壳 `capri-hub/internal/transparent/shell.{go,html}`：构建无关单页，登录 → `/api/hosts` 列表（按 kind）→ opencode 主机 `POST view-ticket` + 表单提交到 `hostOrigin/__capri/session`（target=iframe）加载**官方前端**；shell CSP 的 `frame-src` 限定 `https://*.<hosts-domain>`；`GET /{$}` 精确挂载。
- `cmd/opencode-host` 远程模式：`OCHOST_HUB_URL` + `OCHOST_HOST_TOKEN` 启用 `RunHubDataConnection`（LocalGateway 并行保留）。
- 部署文档 `06-deployment.md`（env、DNS/TLS、运行步骤、真机验收清单）+ 构建脚本 `tools\build-host.ps1`（产出 `dist\{capri-hub,Capri-host,opencode-host}.exe`）。

**已验证**
- `capri-hub` `TestOpencodeRemoteEndToEnd`：**登录 → view-ticket → `/__capri/session`(303) → host 数据连接(`/ws/host/tunnel`, host token) → hostOrigin `GET /global/health` 经隧道 → mock opencode → 200 `{"healthy":true,"version":"1.18.25"}`**。
- `TestShellServed`：`GET /` 返回外壳 HTML、CSP 限定 frame-src。
- 实跑 `capri-hub.exe`：`/` 返回 8093B 外壳；`tools\build-host.ps1` 三二进制全部构建成功。
- `ServeHost` 已排除 `/__capri/` 保留路径（由 hub 处理，不进隧道）——由 e2e 覆盖。

**未验证**：真实 DNS/通配 TLS/手机 iframe/审批/PTY（见 `06-deployment.md` §8 清单）。

## 6. 尚未完成（明确列为待办）

- **真机/公网端到端**：本地已有三进程等价 e2e（见 §4d），但**真实 DNS/通配 TLS/手机浏览器 iframe** 未验证。
- **真实官方前端远程审批 / PTY**：需真机验证「长 SSE 不阻塞审批 POST」「PTY WS 经隧道」。
- **QUIC 数据连接形态**：数据面当前走 WS 降级通道；QUIC per-request stream 未实现。
- **M4 打包/托盘**：Windows 托盘 / macOS 菜单栏未做（已提供三二进制构建脚本）。
- **旧 Grok 客户端统一**：`transport.Client` 仍走旧 events/respond；opencode-host 走独立 `RunHubDataConnection`（设计允许二者并行）。
- **外壳完善**：Grok 主机的既有 capri-fe 视图适配器未接入（当前仅提示）。

## 7. 复现步骤（当前可复现的部分）

```powershell
# 0) Go 环境（工具链在 tools\go，依赖缓存在 capri-host\.gopath）
. D:\1_2027_AI\opencode\tools\goenv.ps1

# 1) 共享库 + 隧道协议测试
cd D:\1_2027_AI\opencode\capri-transport ; go test ./...

# 2) capri-host（含对真实 opencode 的 host 核心集成测试）
cd D:\1_2027_AI\opencode\capri-host ; go test ./internal/hubadapt/... ./internal/ochost/...

# 3) 构建 opencode-host 直连网关
go build -o opencode-host.exe ./cmd/opencode-host

# 4) 直连运行（Basic 由 host 注入，浏览器无需知道）
$env:OPENCODE_SERVER_PASSWORD="change-me"; $env:OCHOST_PUBLIC_PORT="41800"
.\opencode-host.exe
# 浏览器打开 http://127.0.0.1:41800  → 官方 opencode 前端
# curl http://127.0.0.1:41800/global/health  → {"healthy":true,"version":"1.18.25"}
```

## 8. 状态口径

- **设计选择**：独立 hostOrigin、host-only view Cookie、一次性 POST 票据、JSON+base64 隧道块、WS 握手透传与 raw 字节、每 lane 独立窗口、断线不重放（见 01–04）。
- **已实现**：§2、§3、§4 列出的代码。
- **已验证**：§2/§3/§4 的「已验证」条目（含对真实 opencode 与真实 frontend 的探测）。
- **待验证**：真机/公网/DNS/TLS、iframe 真机、QUIC 数据连接、慢网背压、审批端到端、进程树回收。
