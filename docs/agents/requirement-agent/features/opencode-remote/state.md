# State: opencode-remote

## Session Info
- Feature: opencode-remote
- Created: 2026-10-14
- Current Phase: reviewing
- Current Status: waiting_user
- Analysis Iteration: 1
- Review Iteration: 0

## User Requirements Summary
- Core Problem: capri 现在只为 Grok 适配，用户想把自己的 opencode 也做成"手机浏览器远程可用、界面与 opencode 一致"
- Target Users: 单用户自托管开发者（家里/公司/服务器多机）
- Success Criteria: 手机浏览器经统一入口切到某台机器上的 opencode，界面即 opencode 官方前端，流式与审批可远程用；grok 现有能力不回归
- Scope DO:
  - capri 升级为中立多 agent 平台（host kind）
  - 共享传输库 + 透明 HTTP/SSE/WS 平面
  - 新增 opencode-host 二进制
  - 外壳按 kind 分叉视图
  - 鉴权与安全策略
- Scope DO NOT:
  - 统一 API 契约
  - 自研 opencode 前端
  - 原生移动 App
  - 多用户 / 云托管
  - 逐端点映射

## Open Questions
- D5 复核：目标观感是否为 opencode Web 端（非终端 TUI 观感）
- M0 真机未验证项 U1–U7：手机端体验、隧道端到端、SSE/WS 稳定性、Basic+CORS 跨域、多 server、provider 凭证、会话跨设备共享
- capri-host / capri-fe 仓库归属（决定 fork 策略）

## Pending Feedback

| ID | Source | Raw Input | Interpreted Meaning | Complexity | Status |
|---|---|---|---|---|---|
| F-001 | analysis | 需求文档 v0.1 → 并入 capri | 项目定位从"独立 opencode 项目"改为"集成进 capri" | complex | applied |
| F-002 | analysis | D6/D7/D8 三项选定 | 平台中立 + 双二进制 + FE 分叉 | complex | applied |
| F-003 | analysis | M0 实测 | opencode 用 SSE+WS、Basic 鉴权 | simple | analyzed |

## Decisions Summary
- Total decisions recorded in decisions.md: 9

## Codebase Analysis Summary
- 分析对象：`D:\1_2027_AI\opencode\capri-host`（grok 适配版）
- 体量：grok 专用 ≈ 79%（`internal/acp` 476KB + `internal/server` 286KB）；agent-agnostic ≈ 12%（`internal/hub` 105KB + `hubstate`/`netinfo`/`procattr` 12KB）
- 可插拔切口：`bridgeSource` 接口（`internal/hub/bridge.go`）、`Local http.Handler`（`handleRelay`）
- 中继非流式：`relayResponseWriter.Flush` no-op（`internal/hub/client.go:2191`）；`reqFrame` 带 `final` 标志（`client.go:2280`）
- 目标 agent：opencode 1.18.25，`opencode serve` = OpenAPI 3.1 / 162 端点

## Review History
| Iteration | Date | Overall Decision | Business Report | Technical Report |
|---|---|---|---|---|
| 0 | 2026-10-14 | 待评审 | - | - |
