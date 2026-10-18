# opencode-host · 本机服务、代理与监管设计

> 设计修订稿。依赖[C1](03-contract-origin-routing.md)、[C2](03-contract-browser-auth.md)、[C4](03-contract-stream-lifecycle.md)。尚未实现。

## 1. 责任与目录计划

计划新增cmd/opencode-host/main.go和internal/ochost中的supervisor、localproxy、discover、localgateway组件。它们运行在opencode被控机器，复用独立共享传输module；不重新实现会话、模型、工具、权限审批或官方前端业务。

一个host启动入口监管一个opencode serve子进程；两者不是同一个OS进程。serve绑定loopback私有端口。可选LocalGateway另提供一个用户可访问HTTP端口。

## 2. 配置模型

| 配置 | 用途 |
|---|---|
| binPath / cwd | 指定opencode程序和工作目录；优先显式路径，其次PATH/常见安装目录 |
| privatePort | loopback后端端口，不能和公开端口相同 |
| serverUsername / serverPassword | opencode支持的本机Basic身份；用户名规则需按固定版本核对 |
| hubURL / hostId / pairing | 共用已有出站配对模型 |
| hostOrigin | 已认证hub分配的公开origin；供固定版本Origin/CORS兼容配置 |
| localGatewayEnabled / listenAddress / publicPort | 可选直连入口 |
| localFrontendCredential / TLS | 非loopback直连必须有门禁；LAN浏览器路径要求HTTPS |
| startupDeadline / restartBudget | 监管配置，默认见C4 |

本机供应商凭证、项目及会话仍由opencode配置与存储负责。host部署说明需要明确这些配置的取得方式；不能假定只有一个opencode exe就能使用模型。

## 3. Supervisor

Start先定位bin、核对工作目录/端口配置，设置显式loopback监听和本机Basic后启动serve。Health携带相同Basic探测/global/health；默认30秒就绪期限。端口被占不能把陌生服务误当作本次子进程：检查受监管进程存活及配置，发现冲突失败退出/报告。

启动失败或期限到达必须清理该次子进程并等待回收，再有限重试；旧设计“返回失败但保留子进程供重试”删除。运行中进程退出将ready置false，结束依赖它的请求，然后按C4退避和重启预算重启。超额进入failed而非无限重启。

退出时关闭新请求接入、取消网络流、终止并回收进程树；Windows使用Job Object。若bin是脚本/启动器，发现与进程树策略需兼容其实际形式。

## 4. LocalProxy

ForwardHTTP接收method、origin-form target、多值headers和可读body，构造固定loopback请求；它支持增量上传/响应和trailer。ForwardUpgrade需要暴露真正的升级连接与预读缓冲，普通HTTP响应记录器不能承担WS。

代理会删除入站Authorization与网关Cookie，注入本机Basic。该策略属于ochost，shared-transport不读取OPENCODE_SERVER_PASSWORD。HTTP与健康检查/Upgrade统一使用本机身份。

代理按C1处理逐跳头、Host、来源和Forwarded。publicOrigin来自已认证hub；保留真实浏览器Origin/Referer，版本需要时显式允许hostOrigin。iframe前端/API同源，因此不以--cors shellOrigin作为必选配置。

serve提供的状态/头/body按C3返回；业务4xx/5xx原样传回。上游连接失败、读写错误按C4区分响应前后处理。

## 5. 官方前端与LocalGateway

优先转发固定版本serve提供的官方页面和资源；必须确认资源来源和API默认地址。若需要host封装官方构建产物，由视图适配器提供同一版本产物并保留许可；不改业务代码。iframe能力待版本验证。

LocalGateway是本机直连交付项：同一公开端口提供登录、官方前端及HTTP/SSE/WS代理，复用LocalProxy并自身处理门禁；它绕过hub/tunnel。默认loopback，LAN非loopback无门禁fail-fast，并使用HTTPS。loopback HTTP Cookie与公网Secure Cookie区别见C1，不把hub对子域的Cookie复制到本机。

## 6. 与hub状态联动

healthy后声明ready；opencode崩溃时向hub报告ready=false，避免新流进来。健康恢复后允许新reqId；旧请求不重发。kind固定为opencode，本机服务进程是否就绪和出站隧道是否在线独立记录。

浏览器关闭只是网络context/升级连接取消，不自动调用停止任务/删除会话/删除PTY接口；业务影响由opencode定义并验证。

## 7. 未来验收

发现程序、端口冲突、健康认证、启动期限和进程回收、重启预算、HTTP/WS头与字节、凭证隔离、官方iframe、直连fail-fast、hub断线和serve崩溃。当前未实施和测试。
