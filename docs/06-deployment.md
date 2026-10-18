# opencode-remote · 部署与手工验收清单

> 状态：部署步骤依据本轮已实现/已验证的代码整理；标注「未验证」的项需真机/公网执行。

## 1. 组件与拓扑

```
浏览器 ──HTTPS/WSS──▶ capri-hub（公网 VPS）
                        │  · 外壳 + 登录(/api/auth/*) + hostOrigin 路由
                        │  · /__capri/session（票据换 view Cookie）
                        │  · /ws/host/tunnel（host 主动出站的数据面）
   opencode-host（被控机）── 主动出站 ──▶ hub
        └─ 监管 opencode serve（loopback 私有端口）
```

- 连接方向永远是 host → hub（hub 可下发请求）。
- 每个 opencode 主机使用独立子域 origin：`https://<label>.<hosts-domain>`；label 由 hub 分配（`h-<sha256 前10>`），host 不能自选。

## 2. 构建

```powershell
. D:\1_2027_AI\opencode\tools\goenv.ps1   # Go 工具链 + 依赖缓存
# capri-hub
cd D:\1_2027_AI\opencode\capri-hub ; go build -o capri-hub.exe ./cmd/capri-hub
# OpenCode host
cd D:\1_2027_AI\opencode\capri-host
go build -o opencode-host.exe ./cmd/opencode-host
```
`tools\build-host.ps1` 可一次构建 Hub 和 opencode-host 到 `dist/`。

### Windows 一键部署 Hub 到 VPS

仓库根目录执行：

```powershell
.\tools\deploy-hub.ps1
```

脚本使用 `tools\goenv.ps1` 的 Go 工具链与缓存，构建 Linux amd64 版 Hub，上传到
`azureuser@52.253.97.90`，停止旧服务、替换二进制并启动新版本，随后检查服务状态。部署失败时会尝试恢复上一版。
构建时会将当前 Git 短提交号和时间写入 `/api/info` 的 `version` 字段，方便确认线上 Hub 版本。默认 SSH 私钥为
`$env:USERPROFILE\Downloads\xidian-linux_key.pem`。如服务器或密钥路径不同，可覆盖参数：

```powershell
.\tools\deploy-hub.ps1 -Vps "azureuser@服务器IP" -SshKey "D:\\路径\\服务器密钥.pem"
```

部署前需保证 Windows 已安装 OpenSSH Client，SSH 密钥可以免交互登录，且远端账户有
`sudo systemctl` 权限。脚本会保留上一版二进制为 `capri-hub-linux-amd64.bak`；如果新版本
启动失败，会尝试恢复上一版。

### Windows 一键更新 opencode-host

在运行 `opencode-host.exe` 的 Windows 工作电脑上，从仓库根目录执行：

```powershell
.\tools\deploy-opencode-host.ps1
```

脚本会重新构建 `opencode-host.exe`，只停止路径与目标文件完全相同的旧 `opencode-host`，并连同其子进程一起退出；随后替换为新版并启动。默认目标为 `dist\opencode-host.exe`，旧文件保留为 `dist\opencode-host.exe.bak`。如果你当前从其他目录运行程序，可以指定其路径：

```powershell
.\tools\deploy-opencode-host.ps1 -InstallPath "D:\路径\opencode-host.exe"
```

脚本会继承当前 PowerShell 会话的环境变量。运行前需在**同一个 PowerShell 会话**中设置 `OCHOST_HUB_URL` 和已配对的 `OCHOST_HOST_TOKEN`；`OCHOST_HOST_ID`、`OPENCODE_BIN` 和 `OPENCODE_SERVER_PASSWORD` 按需设置。密钥不要写入脚本或提交到 Git。新进程启动后可在它的控制台窗口查看连接日志。

## 3. hub 配置（环境变量）

| 变量 | 说明 |
|---|---|
| `PORT` | hub 监听端口（内置 HTTP；公网应置于 HTTPS 反代之后） |
| `SHELL_ORIGIN` | 外壳 origin，例如 `https://console.example.com`。登录/票据/`/__capri/session` 都要求精确匹配 |
| `HOSTS_DOMAIN` | 主机通配域，例如 `hosts.example.com` |
| `FE_TOKEN` | 浏览器登录密钥（登录会话用）；**不是** opencode 密码 |
| `COOKIE_INSECURE` | 设为 `1` 时 Cookie 不带 `Secure`（仅本机 http 调试用；公网必须留空） |
| `CORS_ORIGINS` | 可选，反向代理跨源访问时设置 |

## 4. DNS 与 TLS（未验证，需真机）

- `console.example.com` → hub 公网地址（A/AAAA）。
- `*.hosts.example.com` → **同一个** hub 公网地址。
- 证书需同时覆盖 `console.example.com` 与 `*.hosts.example.com`（一个通配证书不自动覆盖跨层名称，逐项核对）。
- 前置反代必须：保留 authority、SSE 不缓冲、支持 WS 升级、长连接超时放宽。

## 5. opencode-host 配置（环境变量）

| 变量 | 说明 |
|---|---|
| `OPENCODE_BIN` | opencode 可执行文件（留空则按 PATH/常见目录探测） |
| `OPENCODE_SERVER_PASSWORD` | 拉起 `opencode serve` 的本机 Basic 密码（由 LocalProxy 注入，浏览器永远拿不到） |
| `OCHOST_BIND` | LocalGateway 监听地址，默认 `127.0.0.1`；非回环**必须**同时设 `OCHOST_TOKEN` 否则拒绝启动 |
| `OCHOST_PUBLIC_PORT` | LocalGateway 端口，默认 `41800` |
| `OCHOST_TOKEN` | LocalGateway 直连门禁（回环可留空） |
| `OCHOST_PRIVATE_PORT` | opencode serve 私有端口，默认自动选择 |
| `OCHOST_HUB_URL` | 设置后启用远程：主动出站连 hub |
| `OCHOST_HUB_URL` 的配套 `OCHOST_HOST_TOKEN` | 本机在 hub 上的 host 配对 token（用于换取数据连接票据） |
| `OCHOST_HOST_ID` | 本机标识（本地标签用） |

## 6. 运行步骤

1. 起 hub：`capri-hub.exe`（配好 `SHELL_ORIGIN`/`HOSTS_DOMAIN`/`FE_TOKEN`）。
2. 配对 host：走既有 capri 配对流程（hub 生成配对码 → 本机换取 host token），把得到的 host token 作为 `OCHOST_HOST_TOKEN`。
   - 也可直连本地已有实现复用凭证。
3. 起 opencode-host：
   ```powershell
   $env:OPENCODE_SERVER_PASSWORD="…"
   $env:OCHOST_HUB_URL="https://hub.example.com"
   $env:OCHOST_HOST_TOKEN="…"           # 第 2 步得到的 host token
   .\opencode-host.exe
   ```
   日志出现 `opencode serve ready at http://127.0.0.1:<port>` 与 `data connection established to …/ws/host/tunnel`。
4. 浏览器打开 `https://console.example.com` → 登录（输入 FE_TOKEN）→ 选该 opencode 主机 → 右侧 iframe 加载**官方 opencode 前端**。

## 7. 直连（LocalGateway，不经 hub）

浏览器直接打开 `http://127.0.0.1:41800`（同机）或用 `OCHOST_BIND=0.0.0.0` + `OCHOST_TOKEN` + HTTPS（局域网）。hub 挂掉不影响该路径。

## 8. 手工验收清单（真机/公网，未验证）

- [ ] DNS 通配 + 证书覆盖两种名称；`https://console...` 与 `https://h-...hosts...` 均可访问。
- [ ] 登录成功；未授权 401；错误 Origin 被拒。
- [ ] 选主机后 iframe 内官方前端正常运行（真机手机浏览器）。
- [ ] `GET /global/health` 经隧道返回 `{"healthy":true,...}`。
- [ ] 长 SSE 正常、不缓冲；发 prompt 有流式输出。
- [ ] 权限审批弹窗可远程允许/拒绝；**长 SSE 不阻塞审批 POST**。
- [ ] 终端面板（PTY WS）输入输出正常。
- [ ] 切换主机：旧 iframe 卸载、网络取消；不串机。
- [ ] 退出登录后已开 SSE/WS 被关闭；票据不可二次使用。
- [ ] host 断线重连；hub 不可用时直连路径仍可用。

## 9. 已知未完成
- 完整三进程真机端到端（本地 e2e 测试已覆盖：登录→票据→数据面→mock opencode）。
- QUIC 数据连接形态（当前走 WS 降级通道）。
- Windows 托盘 / macOS 菜单栏打包。
