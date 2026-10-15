# capri-shell · 浏览器外壳与视图路由设计

> 设计修订稿。采用[C1](../03-contracts/origin-routing.md)与[C2](../03-contracts/browser-auth.md)，完整流程见[交互时序图](../04-interactions.md)。

## 1. 承载与职责

浏览器TS应用，计划目录web/shell/，静态资源由hub提供。负责登录、配对/主机管理、在线列表和kind视图选择。服务端的会话/票据/host路由属于capri-hub，不设internal/shell服务。

HostRegistry缓存已认证/api/hosts结果，包括kind、online、ready、origin和capabilities。ViewRouter异步选择视图并处理加载/卸载；原Render同步返回句柄描述不足以覆盖票据交换。

## 2. 组件与状态

| 组件 | 行为 |
|---|---|
| AuthSession | 登录换shell Cookie；保存内存中的CSRF nonce/到期时间；退出与到期恢复登录界面 |
| HostRegistry.List/Refresh | fetch同源host列表，使用shell Cookie；未知host不直接构造URL |
| HostRegistry.ResolveKind | 查hostId的kind；未知/不支持时显示占位 |
| ViewRouter.Render | 异步获取票据、POST到新iframe，挂载opencode视图；Grok走现有视图适配器 |
| ViewRouter.Dispose | 卸载iframe/旧视图，取消外壳自身未完成请求；不停止agent业务 |
| SelectionState | 保存所选hostId；每次恢复仍重新检查身份、主机和协议能力 |

它们是页面会话级实例，不是每条网络请求重新创建。ViewHandle包含hostId/kind、公开baseUrl、挂载元素与dispose操作，不包含本机Basic或host配对token。

## 3. opencode视图加载

1. 从已认证registry取得主机kind及hub分配的origin，验证在线/就绪/能力。
2. 卸载旧视图，创建新的iframe；POST /api/hosts/{hostId}/view-ticket带shell Cookie与CSRF nonce。
3. 在shell内存持有一次性票据，通过POST form提交到hostOrigin/__capri/session，target为新iframe；不写URL或localStorage。
4. hub设置host-only view Cookie，303到/；官方页面、资源和业务请求全部同源经过这个hostOrigin。
5. shell按iframe load事件与hub主机状态展示加载状态；load不代表官方应用业务初始化成功。父页不读取跨origin DOM，不假设未实现的postMessage协议。
6. 票据失效/主机离线/能力缺失/iframe兼容问题展示明确状态并支持重新选择。hub可对HTML导航返回自己的过期/离线说明页。

iframe兼容、CSP和移动端体验目前未验证。不得在iframe无法运行时默默换成自研opencode UI。

## 4. Grok视图

kind=grok加载既有capri视图适配器，继续原Grok API、事件seq与replay行为。实际静态入口和主机选择配置需核对capri-fe/capri-hub源码后记录；该待查项不是已经实现的统一SDK。

## 5. 会话与切换

公网外壳和host子域同site且不同origin；Cookie各自host-only，不共享长期FE_TOKEN。官方前端的普通HTTP、SSE、WS认证由hub验证view Cookie，shell不用逐请求插token。

shell默认60秒检查父会话状态，获取主机状态可先用同源轮询；不要求新增独立业务推送协议。切换主机卸载旧视图后重新兑换目标票据。logout撤销父会话及所有活跃子视图请求。任一取消仅处理网络，业务回合后果按opencode行为判断。

## 6. 未来验收

登录/退出/到期、多host隔离、票据一次性和无URL泄漏、opencode iframe资源/API/SSE/WS、unknown kind、离线/加载失败恢复、跨kind切换。所有均待未来实施验证。
