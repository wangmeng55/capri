# Decisions

## Decision Log

### D-001: opencode 后端走 Server/SDK 路线
- User confirmed: yes
- Source: user discussion
- Decision: opencode-host 直接接本机 `opencode serve`（HTTP+SSE+WS），不走 ACP。
- Rationale: opencode 自带完整 server；走 ACP 会引入翻译层且功能是子集，与"用 opencode 官方前端"自相矛盾。
- Impact: 功能点3 的涉及模块改为 opencode-host + opencode serve。

### D-002: 复用 opencode 官方前端产物
- User confirmed: yes
- Source: user discussion
- Decision: opencode 视图直接使用 `@opencode-ai/app` / `opencode web` 产物，不 fork、不自研。
- Rationale: 满足"界面和 opencode 一样"的最小代价路径。
- Impact: 功能点4 引入"opencode 官方前端（依赖，不修改）"。

### D-003: 中继采用自建透明代理（D3-1）
- User confirmed: yes
- Source: user discussion + M0 实测
- Decision: 在共享传输库新增透明 HTTP 平面，hub 变 per-host 反向代理；D3-2（现成隧道）仅作 M0 spike。
- Rationale: 唯一与"自托管/单一入口/中立 hub"自洽；M0 确认 opencode 用 SSE + WS，逐端点映射不现实。
- Impact: 功能点2 新增 TransparentPlane；关键规则增加"透明代理不解析端点"。

### D-004: 与 capri 关系 = 并入（中立平台）
- User confirmed: yes
- Source: user discussion
- Decision: 不另起项目，直接把 opencode 集成进 capri。
- Rationale: 复用 capri 的传输/配对/鉴权与 hub 模型。
- Impact: 功能点1 引入平台中立化。

### D-005: 目标观感 = opencode Web 端
- User confirmed: yes（待 M0 真机复核 U1）
- Source: user discussion
- Decision: opencode 主机界面目标是 opencode Web 观感（非终端 TUI 观感）。
- Rationale: 浏览器语境下的自然选择。
- Impact: 功能点4 的目标前端为 opencode Web 产物。

### D-006: capri 定位 = 中立多 agent 平台
- User confirmed: yes
- Source: user discussion
- Decision: capri 从"Grok 专用适配"升级为"多 agent 平台"，grok/opencode 平级。
- Rationale: 既然已在动这一层，顺手立成平台，降低后续接入新 agent 成本。
- Impact: 功能点1 引入 host kind。

### D-007: host 形态 = 双二进制（做法1）
- User confirmed: yes
- Source: user discussion
- Decision: `capri-host`（grok）与新 `opencode-host` 各一个二进制，共用抽出的传输库。
- Rationale: grok 主线零回归；两程序各自简单。
- Impact: 关键规则增加"双二进制"；功能点3 新增 opencode-host。

### D-008: FE 形态 = 按 host kind 分叉
- User confirmed: yes
- Source: user discussion
- Decision: 统一外壳 + 各 agent 自有视图；不做统一 API 契约。
- Rationale: 统一契约会把 opencode 压成 grok 形状，且拿不到 opencode 观感。
- Impact: 功能点4 明确"按 kind 分叉"；"明确不做"含统一契约。

### D-009: 首版不做原生移动 App
- User confirmed: yes
- Source: user discussion
- Decision: 首版仅浏览器接入。
- Rationale: 降低范围。
- Impact: "明确不做"含原生移动 App。

## Reflections

### 关键事实（来自 M0 实测）
- opencode server = OpenAPI 3.1 / 162 端点 / 两套命名空间。
- 流式：SSE（`/event`、`/global/event`）；WebSocket（`/pty/{ptyID}/connect`）。
- 鉴权：`OPENCODE_SERVER_PASSWORD`（HTTP Basic）。

### 为什么这决定了很多细节
- 162 端点 → 必须透明代理（功能点2）。
- WS 存在 → 透明平面必须支持 Upgrade（关键规则）。
- Basic 鉴权 → 必须保留 `Authorization`（功能点5）。

### 上下游衔接
- feature name 固定为 `opencode-remote`，供 design-agent / coding-planner 复用。
