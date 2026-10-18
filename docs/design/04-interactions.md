# opencode-remote · 完整交互链路与时序图

日期：2026-10-14。状态：设计修订稿，尚未实现。本文属于设计文档，供开发与验收直接引用。

四份协议定义见 [契约目录](03-contracts-index.md)。本文件展示这些契约如何串成用户可见的流程；端点以固定opencode版本的官方前端实际调用为准，capri不翻译业务端点。

## 1. hub究竟是什么

hub是capri-hub服务，通常部署在一台有公网IP的服务器/VPS上。首版一个hub实例就能中继多台被控机器；浏览器和host都能主动访问它，因此被控机器无需公网IP或端口映射。不同主机的子域指向同一个hub，不增加服务器数量。

```mermaid
flowchart TB
    B[手机或电脑浏览器]
    subgraph VPS[一台公网服务器 / VPS]
        Hub[capri-hub 服务]
        ShellAssets[外壳静态资源与登录/主机管理]
        Routes[按浏览器访问子域定位 host]
        Hub --- ShellAssets
        Hub --- Routes
    end
    subgraph Home[家里的电脑]
        OH[opencode-host]
        OS[opencode serve 子进程]
        Files[本机项目/会话/工具与供应商配置]
        OH -->|loopback HTTP / WS| OS
        OS --- Files
    end
    subgraph Office[另一台电脑]
        GH[capri-host]
        G[Grok]
        GH --> G
    end
    B -->|HTTPS / SSE / WSS| Hub
    OH -->|主动出站 QUIC 或 WSS| Hub
    GH -->|主动出站 旧通道| Hub
```

图中连接建立方向是host→hub，但建立后的隧道双向传数据；hub可以沿这个已存在的连接下发浏览器请求。shared-transport编译在host和相关组件里，是库，不需要再部署一台“传输服务器”。

浏览器UI逻辑在浏览器运行，外壳资源来自hub，opencode官方前端资源经hostOrigin中继取回。本机文件操作、工具执行和模型供应商调用由opencode发起；hub不执行这些命令。hub终止浏览器TLS并处理HTTP，因此能接触中继的请求/响应内容；不宣称它是业务内容不可见的端到端加密节点。

## 2. 首次配对、启动、登记

```mermaid
sequenceDiagram
    participant U as 用户
    participant H as opencode-host
    participant S as Supervisor
    participant O as opencode serve
    participant T as HubClient/透明通道
    participant Hub as capri-hub
    U->>H: 设置hub地址、配对码、工作目录，启动
    H->>S: Start(config)
    S->>O: 启动serve，loopback私有端口与Basic凭证
    S->>O: GET /global/health + Basic
    O-->>S: 健康结果
    alt 就绪
        S-->>H: ready=true
        H->>T: 启动出站连接
        T->>Hub: 首次配对码兑换host token
        Hub-->>T: token与稳定hostId
        T->>Hub: 认证连接，声明kind=opencode和能力
        Hub-->>T: hello与新透明数据会话信息
        T->>Hub: 建立QUIC透明stream能力或WSS数据连接
        Hub->>Hub: 绑定originLabel、hostId、connId，online/ready
    else 启动失败
        S->>O: 清理并回收受监管进程
        S-->>H: 明确失败原因，按有限重试策略处理
    end
```

首次配对兑换沿用旧模型；以后host用本地保存token认证，不反复消耗配对码。hostId与显示名分开；显示名可改，origin保持稳定。新透明连接成功后才能接收新通道请求，opencode健康未通过时不能把主机标成可服务。

opencode-host和opencode serve是两个进程。用户只有一个host启动入口。托盘封装将是后续功能，不应写成这两个进程已经合并。

## 3. 浏览器登录外壳

```mermaid
sequenceDiagram
    participant U as 用户
    participant B as 浏览器/外壳
    participant Hub as hub shell入口
    U->>B: 打开https://console.example.com
    B->>Hub: GET / 外壳静态资源
    Hub-->>B: HTML/JS/CSS登录界面
    U->>B: 输入FE_TOKEN或密码
    B->>Hub: POST /api/auth/login，JSON与精确Origin
    Hub->>Hub: 校验凭证，创建8小时父会话
    Hub-->>B: Set-Cookie __Host-capri_shell，CSRF nonce
    B->>Hub: GET /api/hosts，自动携带shell Cookie
    Hub-->>B: hostId/kind/online/ready/origin/capabilities
    B-->>U: 展示主机选择列表
```

FE_TOKEN仅用来建立登录会话。浏览器后续业务连接使用Cookie，不要求修改官方前端去添加capri请求头。外壳的管理写操作带CSRF nonce；opencode iframe的业务写请求由精确Origin/Referer策略保护。

## 4. 选择opencode主机并加载iframe

```mermaid
sequenceDiagram
    participant U as 用户
    participant Shell as 外壳 console origin
    participant Hub as hub管理/视图入口
    participant I as 新iframe hostOrigin
    participant H as opencode-host
    participant O as opencode serve
    U->>Shell: 选择家里的opencode主机a1
    Shell->>Shell: kind=opencode，准备新iframe
    Shell->>Hub: POST /api/hosts/a1/view-ticket + Cookie/CSRF
    Hub-->>Shell: 绑定a1和父会话的一次性60秒票据、origin
    Shell->>I: POST表单到hostOrigin/__capri/session
    I->>Hub: form票据，Origin=console origin
    Hub->>Hub: 检查目标和父会话，原子消费票据
    Hub-->>I: host-only view Cookie，303重定向到固定/
    I->>Hub: GET /，携带view Cookie
    Hub->>H: C3 http-open/request-end
    H->>O: GET /，本机Basic
    O-->>H: 官方前端页面
    H-->>Hub: response-start/data/end
    Hub-->>I: 官方HTML，允许外壳嵌入
    I->>Hub: 加载JS/CSS与初始API，均为hostOrigin
    Hub->>H: 并发独立reqId请求
    H->>O: 转发资源与API
    O-->>I: 资源和结果逐跳返回
```

票据由hub消费，不交给opencode。iframe中的JS运行origin是主机子域；它的相对API路径也落在这个子域，因此没有shell→opencode的跨源API请求。父外壳只负责切换和登录，不读官方前端内部状态。

官方前端的资源可得性、相对路径与iframe能力尚待固定版本验证。如果此路径不兼容，先修订承载契约，再进入编码，不能默默换成自研UI。

## 5. 一条普通API：读取健康状态

```mermaid
sequenceDiagram
    participant UI as iframe内官方前端
    participant Hub as hub主机子域入口
    participant T as 隧道数据通道
    participant H as host透明请求处理
    participant P as LocalProxy
    participant O as opencode serve
    UI->>Hub: GET /global/health + view Cookie
    Hub->>Hub: 认证/Origin/host映射/ready/配额检查
    Hub->>T: http-open(c1,r1,target,headers,publicOrigin)
    Hub->>T: request-end(r1)
    T->>H: 下发请求与请求体EOF
    H->>P: 固定loopback目标、原path/query
    P->>P: 删除网关Cookie和入站Authorization，注入Basic
    P->>O: GET /global/health
    O-->>P: 200、headers、JSON字节
    P-->>H: 上游响应句柄
    H->>T: response-start(r1,200,headers)
    H->>T: response-data(r1,offset=0,payload)
    H->>T: response-end(r1,finalOffset)
    T-->>Hub: 响应帧
    Hub-->>UI: 状态/头/原JSON体，正常结束
    Hub->>Hub: 清理r1与配额
```

普通HTTP也用分块帧，不把完整响应加载到内存。状态码和多值头先回，再传body。路径/raw query保持原值，hub仅在控制面解析自己的路径，不识别opencode业务端点。

## 6. SSE长连接与一次prompt

```mermaid
sequenceDiagram
    participant U as 用户
    participant UI as 官方前端
    participant Hub as hub
    participant H as opencode-host
    participant O as opencode serve
    participant M as 模型供应商
    UI->>Hub: GET事件端点 + view Cookie
    Hub->>H: http-open(sse1) + request-end
    H->>O: 原SSE请求 + Basic
    O-->>H: 200、text/event-stream
    H-->>Hub: response-start(sse1)
    Hub-->>UI: 写响应头并flush
    U->>UI: 输入prompt
    UI->>Hub: POST官方发送端点 + Cookie
    Hub->>H: http-open(p1)、request-data、request-end
    H->>O: 本机转发prompt
    O->>M: 使用本机供应商配置请求模型
    M-->>O: 模型结果/流式输出
    O-->>H: 在sse1上写事件字节
    H-->>Hub: response-data(sse1)
    Hub-->>UI: 即时写入并flush
    UI-->>U: 更新消息/工具调用界面
    O-->>H: p1自身HTTP响应，语义取决于官方端点
    H-->>Hub: p1 response-start/data/end
    Hub-->>UI: p1完成
    Note over UI,O: SSE继续保持，不能把一次回合结束当作整个SSE结束
```

发送端点可能使用同步或异步机制；本设计不把所有prompt POST强行定义成立即ack。官方前端使用何种端点、响应何时完成，须按固定版本确认。模型连接由被控机器的opencode发起，不通过capri浏览器隧道。

SSE正文只是任意分块字节，隧道不理解message/permission等事件。没有事件时连接可以安静存在；本机已有心跳注释原样经过，capri不会向业务流注入心跳或错误事件。

## 7. 权限审批

```mermaid
sequenceDiagram
    participant O as opencode serve
    participant H as opencode-host
    participant Hub as hub
    participant UI as 官方前端
    participant U as 用户
    O->>O: 某工具操作需要permission，等待审批
    O-->>H: 已有SSE上的permission事件
    H-->>Hub: response-data(sse1)
    Hub-->>UI: 原字节+flush
    UI-->>U: 官方审批UI
    U->>UI: 允许/拒绝
    UI->>Hub: POST /permission/{requestID}/reply
    Hub->>H: 新reqId reply1及body，独立于sse1
    H->>O: reply POST + Basic
    O-->>H: 回执HTTP状态和body
    H-->>Hub: reply1 response-start/data/end
    Hub-->>UI: 回执结果
    O->>O: 按选择执行或拒绝工具
    O-->>H: 后续状态经原SSE
    H-->>Hub: sse1 data
    Hub-->>UI: 更新界面
```

审批POST是新请求，必须能与一直打开的SSE并发。审批端点为M0记录的示例，实际请求以官方前端为准。慢SSE应只对自己的lane施加背压；不能因全局队列阻塞让审批回执永久等待。

## 8. PTY终端WebSocket

```mermaid
sequenceDiagram
    participant UI as 官方前端
    participant Hub as hub WS入口
    participant H as host LocalProxy
    participant O as opencode serve PTY
    UI->>Hub: 创建PTY/取得业务连接票据（官方HTTP）
    Hub->>H: 普通HTTP隧道
    H->>O: 原请求
    O-->>UI: 业务结果逐跳返回
    UI->>Hub: WSS Upgrade + Cookie + Origin + Key
    Hub->>Hub: 检查view认证、Origin和握手结构
    Hub->>H: ws-open(w1)，保留浏览器Key和业务query
    H->>O: HTTP Upgrade + 本机Basic，保留Key
    alt 合法101
        O-->>H: 101 + Accept/Protocol/Extensions
        H-->>Hub: ws-ready(w1,101,headers)
        Hub->>Hub: 校验Accept和协议选择
        Hub-->>UI: 写回上游101，进入字节转发
        UI->>Hub: 终端输入的masked WS字节
        Hub->>H: ws-data(ws-up,offset)
        H->>O: 原升级字节
        O-->>H: 终端输出WS字节
        H-->>Hub: ws-data(ws-down,offset)
        Hub-->>UI: 原升级字节
        UI->>O: close帧经同一字节链路
        O-->>UI: close帧经同一字节链路
    else 拒绝或代理失败
        O-->>H: 实际HTTP拒绝或连接错误
        H-->>Hub: HTTP拒绝帧或stream-error
        Hub-->>UI: 实际拒绝状态/502/504，不能返回101
    end
```

PTY连接票据与capri会话认证各管一层。WS close/ping/pong、frame fragmentation和mask都保留在字节中，不需要把每个内层WS frame变成一个中继JSON。host到hub的outer WSS是另一个传输层，其心跳不进入PTY内容。

## 9. 浏览器离开与取消

```mermaid
sequenceDiagram
    participant UI as 浏览器iframe
    participant Hub as hub
    participant H as opencode-host
    participant O as opencode serve
    UI->>Hub: 关闭页面/取消网络连接
    Hub->>Hub: 将reqId终止，释放浏览器关联
    Hub->>H: cancel(browser_disconnected)
    H->>O: 取消该HTTP context/关闭该升级连接
    H-->>Hub: cancelled
    H->>H: 释放本条连接/缓冲/配额
    Hub->>Hub: 释放配额，保留有界终止墓碑
    Note over Hub,O: 不自动POST停止agent；具体回合是否继续取决于opencode
```

认证到期或logout触发相同的网络取消链路。主机进程不会因为关闭手机页面退出；网络取消的业务后果需要按官方端点核对。

## 10. host断线后重新上架

```mermaid
sequenceDiagram
    participant UI as 官方前端
    participant Hub as hub
    participant H as host
    participant O as opencode serve
    Note over Hub,H: 旧connId=c1断线/失去心跳
    Hub->>Hub: offline或透明通道not ready
    Hub-->>UI: 未提交响应的请求失败；已开流异常断开
    H->>O: 取消旧c1网络请求（检测到断线后）
    H->>Hub: 退避后重新连接/认证/协商
    Hub-->>H: 新数据connId=c2
    Hub->>Hub: online/ready恢复
    UI->>Hub: 官方前端重查状态/重建事件流
    Hub->>H: c2的新reqId
    H->>O: 全新HTTP请求
    Note over UI,O: 不重发旧POST，不续传旧PTY字节；是否补事件由opencode决定
```

恢复网络与恢复会话数据是两件事。opencode本机持久会话可供重新查询，但中继不保证断线期间的所有SSE增量都能重现。

## 11. 主机切换

```mermaid
sequenceDiagram
    participant U as 用户
    participant Shell as 外壳
    participant A as 旧opencode iframe
    participant Hub as hub
    participant B as 新视图
    U->>Shell: 切换到另一台主机
    Shell->>A: 卸载旧iframe
    A->>Hub: 浏览器取消旧HTTP/SSE/WS
    Hub->>Hub: 按C4取消旧流
    Shell->>Hub: 获取新host的状态与kind
    alt kind=opencode
        Shell->>Hub: 获取并兑换新hostId的view-ticket
        Hub-->>B: 该hostOrigin Cookie及官方前端
    else kind=grok
        Shell->>B: 加载既有capri视图适配器
        B->>Hub: 使用原Grok API/事件契约
    end
    B-->>U: 展示所选主机视图
```

切换保存的是用户选择和各host自己已有会话。它不把两种agent会话格式互转；不会把新opencode tunnel流控施加到Grok事件面。Grok视图入口和选择配置需查capri-fe源码确认。

## 12. 本机/局域网直连（独立交付项）

```mermaid
sequenceDiagram
    participant B as 浏览器
    participant G as host LocalGateway
    participant P as LocalProxy
    participant O as loopback opencode serve
    B->>G: 直接打开host公开端口，登录直连门禁
    G-->>B: 本origin会话Cookie和官方前端
    B->>G: 同源HTTP/SSE/WS
    G->>P: 本地请求，验证Cookie/Origin
    P->>O: 固定目标+Basic
    O-->>P: 响应/升级流
    P-->>G: 增量返回
    G-->>B: 直接响应，不经过hub
```

LocalGateway和opencode私有端口分别监听。局域网非loopback绑定需要直连认证和HTTPS；公网外壳自动选择LAN路径另需处理浏览器限制，不属于首条远程链路。

## 13. 未来实施顺序与通过条件

| 阶段 | 工作 | 通过后才进入下一阶段 |
|---|---|---|
| D0 设计与版本检查 | 补齐C1–C4、固定前后端版本，核对iframe/Origin/官方资源/其他仓库 | 关键前提有证据，失败则修订设计 |
| M1 共享库抽取 | 明确module/import布局，去掉Grok具体类型依赖；保留旧协议 | capri-host仍可编译、既有Grok行为通过回归 |
| M2a 普通API | 单host隧道能力协商、C3 HTTP帧，先GET /global/health | 状态/重复头/query/二进制/限额/取消正确；Basic仅本机 |
| M2b SSE | 相同HTTP帧增量响应与flush | 即时事件、长连接不累计缓存；取消/慢网/认证到期处理正确 |
| M2c WS | Upgrade握手透传与双向字节流 | PTY输入输出、拒绝、close、较大frame、断线正确 |
| M3a 审批 | 官方前端发prompt与permission reply | 手机收到审批并生效；SSE与POST不会互相堵塞 |
| M3b 主机切换 | 外壳票据/iframe与kind适配器，多host | opencode↔opencode及opencode↔grok切换，Cookie/请求不串机 |
| M4 直连/打包 | LocalGateway、Windows托盘、部署说明 | 直连认证、hub离线可用、进程生命周期符合契约 |

本轮仅完成设计修订。表中所有验证都是未来实施验收，没有声称当前已通过。
