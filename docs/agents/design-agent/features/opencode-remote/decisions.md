# 设计决策与修订记录 · opencode-remote

## 1. 已有用户决策

需求中已锁定中立多agent平台、并入capri、双host二进制、共享传输库、Server路线、官方opencode前端、按kind分叉、自建透明代理、首版浏览器/单用户自托管。原始记录保存在[需求决策](../../../requirement-agent/features/opencode-remote/decisions.md)。

本轮用户明确要求写清四份契约、把链路图纳入设计，并强调只做设计。下面记录为该授权范围内的技术方案；不伪称用户已逐项确认，也不引入额外确认流程阻止文档完成。

## 2. 本轮具体方案

| 编号 | 选择 | 理由/影响 |
|---|---|---|
| AD-01 | 一台公网hub提供shell和多个HTTPS主机子域，originLabel由hub登记 | host不需要公网IP；origin路由必须在hub校验，独立origin不等于多台服务器 |
| AD-02 | opencode官方前端在hostOrigin的iframe内运行，业务API同源 | 保留统一外壳；iframe/CSP/资源兼容性仍需实证 |
| AD-03 | FE_TOKEN换shell Cookie，一次性POST票据换host-only view Cookie | 覆盖EventSource/WS不支持任意认证头的情况；主机间不共享Domain Cookie |
| AD-04 | LocalProxy注入本机Basic并删除入站网关身份 | 共享库中立，host凭证留本机；修正原“保留浏览器Authorization即可” |
| AD-05 | http-tunnel-v1独立能力与数据平面，所有HTTP逐块传输 | 响应头/body/上传/取消/窗口有完整契约，旧respond不扩展业务语义 |
| AD-06 | 首版JSON+base64数据块、decoded≤64KiB；多值HTTP头 | 便于协议落地并保留二进制、重复头；存在33%编码开销，预算需实测 |
| AD-07 | WS握手透传；升级后原始字节转发 | 保留browser key/accept、mask/close/ping/fragment；不强制两个独立WS应用握手 |
| AD-08 | 每lane独立credit、有界缓冲与公平调度，不丢透明payload | HTTP/SSE/WS任意丢字节会破坏流；Grok可丢事件不套用到此 |
| AD-09 | 数据连接断线结束旧reqId，不自动重放 | 避免POST重复副作用；Grok seq/replay与opencode SSE恢复分开 |
| AD-10 | Supervisor失败清理受监管进程，再有限重试 | 删除原就绪失败但保留子进程的模糊生命周期 |
| AD-11 | 单启动入口、一个用户公开HTTP端口，可选LocalGateway+私有serve端口 | 监管serve实际是多进程；直连需显式HTTP入口与门禁 |
| AD-12 | 普通API→SSE→WS→审批→主机切换的分阶段验收 | 先建立基础传输证据；M1共享库/Grok回归先于新链路 |

## 3. 修正此前说明

- 普通“前端跨origin所以浏览器不会有Basic”的表述不足以定义认证。此方案本来就不把本机Basic交给浏览器，通过hub Cookie和host本机注入分别完成两层认证。
- WebSocket透明代理不必建立两个分别生成key的独立握手。本方案转发browser key及server accept，再传原始升级字节；详细规则见C3。
- /h/x/返回SPA只能说明没有证明base path可用，不能据此宣布源码已证明所有路径前缀方案不可能；独立origin是选定设计。
- “透明代理天然免疫版本漂移”修正为降低端点适配成本；前端构建、认证和协议行为仍需要兼容性检查。
- 任意流式帧不可以按agent事件语义丢弃；response头提交后也不能回写新的504。
- 原单进程、同步Render、Map<string,string>响应头和Enqueue(reqId,payload,final)描述不足以覆盖实际链路，已同步模块设计。

## 4. 依据与证据边界

接口约束引用HTTP/WebSocket/EventSource/Cookie公开规范，链接位于C2/C3。M0的API/SSE/WS/Basic发现仍是历史本机实测记录；新增部署、Cookie、iframe、流控和默认限额均未实施验证。

正式设计评审、手机真机、隧道端到端与Grok回归尚未执行。当前只交付可审阅的设计文档。
