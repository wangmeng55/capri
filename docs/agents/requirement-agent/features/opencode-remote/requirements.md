# capri 多 agent 平台 · opencode 远程接入

## 概述
把 opencode 集成进 capri，使 capri 从中立化视角支持多 agent：用户在自己机器上运行的 opencode，可通过手机/其它设备的浏览器远程使用，且界面就是 opencode 自己的前端；同时不破坏现有 grok 能力。

## 功能点1：平台中立化与 host 类型

### 优先级
必须做

### 涉及模块
- **capri-shell（新增）**
  - 文件路径：`web/shell/`（浏览器端 TS 应用；静态资源由 capri-hub 托管）
  - 职责：统一外壳（切换/配对/登录）+ 按 host kind 渲染视图
  - HostRegistry：从 hub 拉取并缓存 host 清单与 kind
  - ViewRouter：按 kind 选择并挂载视图
- **capri-hub（修改）**
  - 文件路径：`capri-hub/`（独立仓库）
  - 职责：登记 host 及其 kind，向 FE 提供 host 列表
  - RelayServer：接受 host 出站连接与登记

### 流程步骤
1. [浏览器] 通过 [页面操作] 在外壳（`web/shell/`）中发起 [新增主机/切换主机]
2. [外壳] 通过 [HTTP API] 向 [capri-hub] 请求 [host 列表（含 kind）]
3. [外壳] 通过 [前端路由] 依据 [kind] 加载 [capri 视图 / opencode 视图]

### 讨论澄清
- capri 定位改为"中立多 agent 平台"（D-006）
- host 形态采用双二进制、共用传输库（D-007）
- **外壳定位**：浏览器端 TS 应用，由 capri-hub 托管静态资源；**不设服务端 `internal/shell/`**。host 的 kind 由 host 在注册时声明给 hub。

## 功能点2：共享传输库与透明 HTTP 平面

### 优先级
必须做

### 涉及模块
- **shared-transport（修改）**
  - 文件路径：`internal/transport/`（自 `capri-host/internal/hub` 抽出）
  - 职责：提供与 agent 无关的传输与透明 HTTP 平面
  - TransparentPlane：**新增**透明 HTTP 平面，透传任意请求（含 SSE / WebSocket 升级）
  - HubClient：出站连接、鉴权、配对、事件 seq/replay/背压（grok 原通道，**不修改**）
  - FrameCodec：新增流式帧编解码

### 流程步骤
1. [capri-hub] 通过 [中继通道] 向 [TransparentPlane] 发送 [原始请求（method/path/headers/body）]
2. [TransparentPlane] 通过 [HTTP 转发] 将请求交给 [host 本地服务]，并注入 [Authorization 头]
3. [host 本地服务] 通过 [流式响应] 回传 [SSE 或 WebSocket 字节流]
4. [TransparentPlane] 通过 [多帧流式] 将响应回传 [capri-hub]，直到 [final 帧]

### 讨论澄清
- 中继采用自建透明代理 D3-1（D-003）
- WebSocket 为硬性要求（M0 实测 opencode 终端面板走 WS）
- **透明平面是新增的一条并行通道**：grok 继续走原 `respond` 单帧通道，行为不变

## 功能点3：opencode host 接入

### 优先级
必须做

### 涉及模块
- **opencode-host（新增）**
  - 文件路径：`cmd/opencode-host/`、`internal/ochost/`
  - 职责：拉起本机 opencode server，并将其 API 经中继暴露
  - OpencodeSupervisor：spawn/监管 `opencode serve`
  - LocalProxy：把中继请求转发到 loopback opencode server
  - DiscoverOpencodeBin：按常见路径/PATH 发现 opencode 可执行文件
- **shared-transport（依赖，不修改）**
  - 文件路径：`internal/transport/`
  - 职责：提供传输与透明平面
- **opencode serve（依赖，不修改）**
  - 文件路径：`opencode` 二进制（外部）
  - 职责：opencode 官方 HTTP + SSE + WebSocket 后端

### 流程步骤
1. [opencode-host] 通过 [进程 spawn] 拉起 [opencode serve（loopback）]
2. [opencode-host] 通过 [健康探测 /global/health] 确认 [server 就绪]
3. [opencode-host] 通过 [shared-transport] 向 [capri-hub] 出站注册 [hostId + kind=opencode]
4. [LocalProxy] 通过 [HTTP 转发] 将 [中继请求] 转给 [opencode server]，原样回传响应

### 讨论澄清
- 后端走 Server/SDK 路线，直接接 `opencode serve`（D-001）
- 复用 opencode 官方前端产物，不自研（D-002）

## 功能点4：前端按 kind 分叉

### 优先级
必须做

### 涉及模块
- **capri-shell（新增）**
  - 文件路径：`web/shell/`（浏览器端 TS）
  - 职责：统一外壳 + 按 kind 渲染视图
  - ViewRouter：grok→capri 视图；opencode→opencode 前端产物
- **opencode 官方前端（依赖，不修改）**
  - 文件路径：`@opencode-ai/app` / `opencode web` 产物
  - 职责：opencode 的原生浏览器界面

### 流程步骤
1. [用户] 通过 [页面操作] 在外壳中选择 [某台 opencode 主机]
2. [ViewRouter] 通过 [前端路由] 加载 [opencode 前端产物] 并挂载
3. [opencode 前端] 通过 [HTTP + SSE + WS] 经 [hub → opencode-host] 与 [opencode server] 通信

### 讨论澄清
- 前端按 host kind 分叉，不做统一契约（D-008）
- 目标观感为 opencode Web 端（D-005）
- **承载方式**：每台 host 使用**独立 origin（子域或子端口）**承载 opencode 前端；**不可用 URL 路径前缀**（M0 实测 `GET /h/x/` 只是 SPA catch-all，不能证明 base path 支持）。跨源由 `opencode serve --cors <shell-origin>` 兜底（M0 实测可开启）。

## 功能点5：鉴权与安全

### 优先级
必须做

### 涉及模块
- **capri-hub（修改）**
  - 文件路径：`capri-hub/`
  - 职责：浏览器侧门禁（FE_TOKEN / 密码）
- **shared-transport（修改）**
  - 文件路径：`internal/transport/`
  - 职责：由 host 侧注入 `Authorization` 头
- **capri-shell（新增）**
  - 文件路径：`web/shell/`
  - 职责：外壳登录态与本机 API 鉴权

### 流程步骤
1. [浏览器] 通过 [请求头] 向 [capri-hub] 出示 [FE_TOKEN]
2. [capri-hub] 通过 [校验] 决定 [放行/拒绝] 并将请求转发 [host]
3. [host 侧 TransparentPlane] 通过 [注入 Basic] 使 [opencode server] 的鉴权生效
4. [非回环绑定且无 token] 时 [host] 通过 [fail-fast] 拒绝启动

### 讨论澄清
- opencode 鉴权为 HTTP Basic（`OPENCODE_SERVER_PASSWORD`），凭证在 **host 本机**（M0 实测）
- **是"注入"不是"保留"**：浏览器跨源访问 host 时不会携带该 Basic 凭证，必须由 host 侧补上

## 功能点6：部署与打包

### 优先级
可以做

### 涉及模块
- **opencode-host（新增）**
  - 文件路径：`cmd/opencode-host/`
  - 职责：单进程单端口形态；托盘/菜单栏可拉起
- **capri-tray（修改）**
  - 文件路径：`cmd/capri-tray/`
  - 职责：进程监管与设置界面

### 流程步骤
1. [用户] 通过 [双击/托盘] 启动 [opencode-host]
2. [opencode-host] 通过 [配置加载] 读取 [端口/hub 地址/token]
3. [opencode-host] 通过 [单端口] 同时提供 [API + 嵌入前端]

### 讨论澄清
- 首版不覆盖原生移动 App，仅浏览器（D-009）

## 功能点7：危险操作远程审批

### 优先级
必须做

### 涉及模块
- **shared-transport（依赖，不修改）**
  - 文件路径：`internal/transport/`
  - 职责：透明平面承载审批事件与回执
- **opencode-host（依赖，不修改）**
  - 文件路径：`internal/ochost/`
  - 职责：本地转发审批请求
- **opencode 官方前端（依赖，不修改）**
  - 文件路径：`@opencode-ai/app` 产物
  - 职责：呈现审批 UI

### 流程步骤
1. [opencode server] 通过 [SSE] 经 [透明平面] 向 [浏览器] 推送 [permission 请求]
2. [用户] 通过 [页面操作] 在 [opencode 官方前端] 点击 [允许/拒绝]
3. [opencode 前端] 通过 [HTTP POST] 经 [透明平面] 向 [opencode server] 回传 [`/permission/{requestID}/reply`]

### 讨论澄清
- 审批为纯 HTTP（M0 实测端点），由透明平面承载，无需独立模块

## 关键规则
- **agent 无关传输**：传输库不得包含任何 agent 语义；grok 与 opencode 一视同仁
- **透明代理不解析端点**：中继不得逐端点映射，以免疫 opencode 版本漂移
- **透明平面为新增并行通道**：grok 走原 `respond` 通道，行为不变；两者并行不互相影响
- **流式不受单帧上限约束**：透明平面的流式通道不得套用单帧响应上限，改按帧计数/时间窗口限流
- **鉴权头注入**：opencode 用 HTTP Basic，凭证在 host 本机；由 host 侧**注入**（浏览器跨源时不带该头）
- **每 host 独立 origin**：不以 URL 路径前缀承载 opencode 前端
- **流式与升级支持**：必须支持 SSE 与 WebSocket 升级，SSE 不能被缓冲成单帧
- **grok 零回归**：抽库仅换 import，grok 行为不变
- **双二进制**：grok 与 opencode 各一个 host 二进制，共用传输库
- **外壳为浏览器端**：`web/shell/`（TS）；无服务端 `internal/shell/`
- **单一入口**：浏览器经同一外壳切换主机

## 明确不做
- **统一 API 契约**：不把 grok/opencode 翻译成同一套 `/api/*`（D-008）
- **自研 opencode 前端**：复用官方产物，不自研仿制（D-002）
- **原生移动 App**：首版仅浏览器
- **多用户/云托管**：维持单用户自托管模型
- **逐端点映射**：不做端点级适配层
- **URL 路径前缀承载前端**：每 host 用独立 origin
