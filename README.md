# WFU Seat Reservation

面向智慧潍院／超星座位服务的终端客户端与独立后端。主程序使用 Go、Bubble Tea 和 SQLite，提供扫码登录、账号隔离、时段选择、最多三个候选座位，以及按教室开放窗口执行的持久化预订。

Python Notebook 保留为只读学习原型。项目没有网页前端；远程模式通过 HTTPS JSON API 与 TUI 通信。

> 本项目不是学校或超星的官方客户端。只管理本人授权的账号，并遵守学校预约规则。创建并确认 Go 预订任务后，调度器会向学校发送真实请求；浏览和查询不会提交预约。常规自动化测试完全使用模拟学校服务。

## 目录

- [三种版本](#三种版本)
- [构建与运行](#构建与运行)
- [扫码与界面操作](#扫码与界面操作)
- [预约执行规则](#预约执行规则)
- [远程部署](#远程部署)
- [数据与身份隔离](#数据与身份隔离)
- [HTTP API](#http-api)
- [项目结构](#项目结构)
- [开发与测试](#开发与测试)
- [发布前检查](#发布前检查)
- [常见问题](#常见问题)
- [能力边界](#能力边界)

## 三种版本

| 版本 | 文件前缀 | 用途 |
| --- | --- | --- |
| 完整版 | wfuseat | 本地 TUI＋本地调度，也能连接远程后端或以服务端模式运行 |
| 仅 TUI | wfuseat-tui | 远程界面壳；不启动调度器，编译时禁止直接访问学校服务与执行预约 |
| 仅后端 | wfuseat-server | 学校登录、查询、身份验证、任务存储与调度，无 TUI |

仅 TUI 版可以通过 API 创建、暂停和取消后端任务，实际预约由后端执行。完整版本的本地模式和服务端模式是不同启动方式，不会在一次启动中同时展示 TUI 和提供 HTTP 服务。

## 构建与运行

### 环境要求

- Go 1.25.9 或兼容该 go.mod 的更新版本。
- 支持 UTF-8 的终端，建议 Windows Terminal 或常见 Linux 终端。
- Python 3 用于构建脚本；使用 Notebook 时需要 Python 3.13+。
- 编译首次下载依赖需要网络。普通使用 Go 可执行文件无需安装 Python。

### 编译

~~~~bash
# 当前平台完整版
go build -o wfuseat .

# 当前平台仅 TUI；构建标签不能省略
go build -tags wfuseat_tui -o wfuseat-tui ./cmd/wfuseat-tui

# 当前平台独立后端
go build -o wfuseat-server ./cmd/wfuseat-server

# 一次生成 Linux / Windows amd64 的三种版本
python3 scripts/build.py
~~~~

批量构建输出到 dist/，并更新项目根目录的当前平台完整版。文件名：

~~~~text
dist/
  wfuseat-linux-amd64
  wfuseat-tui-linux-amd64
  wfuseat-server-linux-amd64
  wfuseat-windows-amd64.exe
  wfuseat-tui-windows-amd64.exe
  wfuseat-server-windows-amd64.exe
~~~~

构建产物不纳入源码仓库。

### 本地使用

~~~~bash
./wfuseat
./wfuseat --account 张20260001
./wfuseat --list-accounts
./wfuseat --account 张20260001 --jobs
~~~~

首次进入账号面板扫码。登录状态会保存在本机，后续启动先验证已保存的会话，失效时再扫码。账号名从学校返回的姓名与学号生成，格式为“姓＋学号”，支持常见复姓。

本地版默认代理为 http://127.0.0.1:7890，可在账号详情修改。明确使用直连：

~~~~bash
./wfuseat --proxy ""
~~~~

### 后端与仅 TUI

先启动后端：

~~~~bash
# 默认直连，监听本机 8787
./wfuseat-server --listen 127.0.0.1:8787

# 学校入口需要网络代理时，在后端配置
./wfuseat-server --listen 127.0.0.1:8787 --proxy http://127.0.0.1:7890

# 完整版也可提供同样的后端
./wfuseat --serve --listen 127.0.0.1:8787
~~~~

再启动客户端：

~~~~bash
# 首次启动在 TUI 输入后端 URL；回车保存，Esc 退出
./wfuseat-tui

# 也可以指定并保存地址
./wfuseat-tui --server http://127.0.0.1:8787

# 以后直接运行，自动使用上次地址
./wfuseat-tui

# 重新选择地址
./wfuseat-tui --choose-server
~~~~

后端 URL 和网络代理是两个不同的配置。客户端选择的是后端服务；后端的 proxy 决定怎样访问学校。

仅 TUI 版将上次地址保存在配置根目录的 client.json。不同后端的登录凭证分开保存。使用 --config-dir 可为运行实例指定独立配置根目录；完整版不指定 --server 时始终保持本地模式。

## 扫码与界面操作

1. 在账号面板按 n，右侧立即展开二维码。
2. 用学校 App 扫码并确认；未完成时按 Esc 取消，不留下待登录账号。
3. 选择教室，再选择具体日期或“自动”。
4. 在右侧选时间端点，再按顺序选座位。
5. 按 a 查看预订确认信息，确认后写入任务队列。

窗口无法容纳二维码时，支持的 Windows／WSL 环境会尝试打开独立全屏二维码窗口。二维码及临时文件属于登录凭证，不应分享或提交。

### 常用按键

| 按键 | 作用 |
| --- | --- |
| 0–4 | 切换账号、教室、时段、座位、预订记录面板 |
| 5 | 查看选座预览 |
| Tab / Shift+Tab | 切换左右区域；自动规则页按其焦点顺序移动 |
| hjkl / 方向键 | 移动；具体方向由当前列表或网格决定 |
| Space | 选中或取消当前项 |
| Enter | 进入当前详情／确认当前步骤 |
| Esc | 返回或取消当前操作 |
| n | 在账号面板新增扫码账号 |
| p | 在账号右侧详情切换预订开关 |
| D | 在账号右侧发起删除确认 |
| Ctrl+S | 保存账号详情配置 |
| a | 将当前候选加入自动预订 |
| u | 打开账号管理 |
| r | 刷新当前数据或重新获取二维码 |
| / | 过滤教室或座位 |
| d / ? / q | 诊断信息／帮助／退出 |

### 时间与候选座位

- 选择一个时间端点：预约该时段。
- 选择两个端点：从较早时段开始到较晚时段结束，包含首尾。
- 不允许跨越不连续或暂停时段；第三个端点需先取消已有端点。
- 修改时间区间会清空旧候选，避免时间与座位选择错配。
- 最多三个候选座位，按照选中顺序执行。

固定时段遵循学校返回的列表；连续时段按学校返回的 timeUnit 拆分。timeUnit 为 0 时不擅自把整段拆开。

### 自动规则

“自动”和各个具体日期是同级选项，不需要先选某个日期才能设置循环。进入自动后会主动查询参考日期的可用时段。

- 每天：所有日期使用共同时间区间。
- 周一～周五：工作日使用共同时间区间。
- 按周循环：每个星期几独立配置区间。

按周模式中，h／左箭头进入星期选择，l／右箭头进入时段，j/k 在当前区域移动。在时段区域按 Enter 前往下一天，不跳到座位。选择有效时间区间会自动启用对应星期，清空端点则取消该星期。

当前新建规则持续循环，直到取消、暂停或出现需要处理的状态；已移除持续 X 天选项。历史有限循环与 cron 数据保留兼容处理，但当前 UI 使用上述三种规则。

## 预约执行规则

- 开放时间从学校教室预约窗口接口动态获取，按教室和日期分别计算，不写死 22:15。
- 原始开放时间戳不额外增加一秒。
- 任务持久保存在 SQLite，执行前事务领取，避免重复执行同一任务发生项。
- 后台使用独立会话，界面切换教室或日期不会改变已保存任务。
- 固定延迟单位为毫秒，范围 0–60000；配置多少就等待多少，不再随机抽取。
- 延迟发生在登录校验与预约准备之前，不等价于网络请求精确到毫秒到达学校；候选之间不重复延迟。
- 只有服务端明确拒绝，才立即尝试下一个候选。
- 请求超时、连接中断或结果不明确时停止换座，避免重复预约。
- 程序错过计划时刻时不会任意补发：超过调度器容忍窗口会记录错过；循环按其规则推进。
- 账号关闭预订后暂停后续执行。取消或删除不会撤销已经在学校成功的预约。

本地完整版退出后，本地调度停止。可以使用独立 worker 持续执行本地账号任务：

~~~~bash
./wfuseat --worker
~~~~

远程模式关闭客户端不会停止后端调度。后端必须持续运行。

## 远程部署

后端默认只监听回环地址。不提供 TLS 时拒绝监听公网／非回环地址，客户端也拒绝远程明文 HTTP。

### 直接提供 HTTPS

~~~~bash
./wfuseat-server --listen :9443   --tls-cert /path/to/fullchain.pem   --tls-key /path/to/privkey.pem   --config-dir /path/to/private-state
~~~~

客户端连接 https://seat.example.com:9443。证书应被客户端系统信任，程序没有跳过证书校验的开关。

### HTTPS 反向代理

也可以由反向代理处理 HTTPS，转发到本机 127.0.0.1:8787。需保留 Authorization 和 Idempotency-Key 请求头，并允许学校登录回调执行至少 100 秒。

后端不信任客户端提供的 X-Forwarded-For。内置并发上限、每身份限流和扫码入口限流；反向代理后，扫码入口的 IP 限额可能由多个用户共享。应结合部署环境设置入口限流和访问日志脱敏。

当前推荐单个常驻后端进程，由系统进程管理器负责重启。未验证多节点部署、共享网络文件系统或高可用集群。

## 数据与身份隔离

远程服务没有独立注册密码。扫码完成后，后端从认证后的学校页面读取学号，以学校标识＋学号派生内部账号 ID。每个业务请求都依据后端会话确定归属，不接受客户端自报 owner 作为权限依据。

- 学校 Cookie、签名参数和执行日志留在后端。
- 客户端只持有后端发放的会话凭证，当前有效期为 30 天。
- 后端持久化会话 token 的哈希；客户端 token 文件属于敏感文件。
- 不同账号使用独立配置和数据库。
- 重新登录当前账号时扫码身份必须一致。
- 删除远程账号会删除该后端的凭证与任务，并撤销所有客户端会话；重新扫码不会恢复旧任务。
- 对任务的查看、取消、执行历史查询都校验账号归属。
- HTTP 客户端不跟随 API 重定向，避免向其他地址传递 Bearer。

默认配置根目录由操作系统决定，Linux 常见位置为 ~/.config/wfuseat。可使用 --config-dir 或 WFUSEAT_CONFIG_DIR 指定。

~~~~text
<配置根目录>/
  client.json                         仅 TUI 记忆的后端 URL
  accounts/<姓＋学号>/                 本地模式账号
    config.json
    state.db
    logs/
  remotes/<后端地址哈希>/
    accounts/<内部账号 ID>/
      config.json
      session.json                    后端会话 token
      request-*.pending               尚未确认的 API 请求标识
  server/
    auth/state.db                     身份及后端 token 哈希
    data/accounts/<内部账号 ID>/
      config.json
      state.db                        学校 Cookie、任务、执行结果
      logs/
~~~~

Unix 环境账号目录限制为 0700，敏感文件限制为 0600；Windows 还依赖运行用户和目录 ACL。数据未使用独立的应用层静态加密，后端管理员可访问学校凭证，应仅连接可信后端。

### 每账号 UA

每个账号保存独立 user_agent。浏览器部分保持兼容登录，附加不含姓名、学号的 WFUseat 标记。登录、回调、查询、开放时间刷新和实际执行保持当前账号 UA，不按请求随机切换。

恢复会话和持有有效后端 token 的重新扫码会沿用原 UA。新设备尚未认证时无法预先知道是谁扫码，会为此次扫码生成 UA，确认后随学校会话保存。UA 不改变出口 IP，也不能保证不触发平台限制。

## HTTP API

接口使用 /v1 前缀，返回 JSON。除健康检查和创建扫码入口外，业务接口需要 Authorization: Bearer <后端 token>。扫码轮询使用独立的扫码 secret。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | /v1/health | 协议版本 |
| POST | /v1/login-sessions | 返回扫码 id、secret 和 Base64 PNG |
| GET | /v1/login-sessions/{id} | 轮询扫码；确认后返回后端 token |
| DELETE | /v1/login-sessions/{id} | 取消扫码 |
| GET | /v1/me | 本人身份、配置及调度提示 |
| PUT | /v1/settings | 设置 allow_submit、delay_ms |
| POST | /v1/query | home / rooms / room / seats 查询 |
| GET | /v1/jobs | 本人任务 |
| POST | /v1/jobs | 创建任务，必须提供 Idempotency-Key |
| DELETE | /v1/jobs/{id} | 取消本人未执行任务 |
| GET | /v1/jobs/{id}/runs | 本人任务运行记录 |
| POST | /v1/logout | 撤销当前后端会话 |
| DELETE | /v1/me | 删除后端账号并撤销全部会话 |

只读查询示例；TOKEN 为运行时提供的后端 token，不应写入脚本或提交：

~~~~bash
curl -H "Authorization: Bearer $TOKEN" https://seat.example.com/v1/me
curl -H "Authorization: Bearer $TOKEN" https://seat.example.com/v1/jobs
~~~~

POST /v1/query 的示例负载：

~~~~json
{
  "operation": "room",
  "day": "2030-01-01",
  "room_id": 6299
}
~~~~

示例日期、座位、学号均为说明或测试数据，使用时应从当前服务查询真实可用值。

创建任务使用 room_id、day、start、end、seats。循环任务另外提供 mode（daily、weekdays、weekly）及 week_plan，后者为按周一到周日排列的七个对象，各包含 enabled、start、end。

后端会重新校验身份、教室、时间区间和座位。相同幂等键与相同负载重复提交只创建一次；相同键配不同负载返回 409。TUI 会保留未确认请求的键，重启后再次确认相同任务仍可安全重试。

## 项目结构

~~~~text
main.go                         完整版入口
cmd/wfuseat-tui/                 仅 TUI 入口
cmd/wfuseat-server/              独立后端入口
internal/
  api/                          协议、远程客户端、客户端凭证
  apiserver/                    后端认证、授权及 HTTP 路由
  chaoxing/                     学校登录、查询、签名、网络保护
  config/                       配置与账号 UA
  launcher/                     远程启动及后端地址输入界面
  schedule/                     任务时间、开放窗口、执行与恢复
  scheduleclock/                旧时间字段的兼容校验
  servercmd/                    共用后端启动逻辑
  storage/                      SQLite、账号目录与持久化
  tui/                          Bubble Tea 界面、交互和集成测试
scripts/build.py                三版本交叉编译
scripts/check_publication.py    待提交内容检查
00_qr_login.ipynb                Python 扫码实验
01_offline_signature.ipynb       离线签名实验
02_login_to_selection.ipynb      Python 只读选座链路
tests/                          Python 离线测试
~~~~

## 开发与测试

~~~~bash
go test ./...
go test -race ./...
go vet ./...
go test -tags wfuseat_tui ./internal/chaoxing -run TestThinClient
~~~~

测试覆盖：扫码状态机、Cookie 恢复、多账号隔离、代理错误提示、时间区间、周计划、候选顺序、明确拒绝／未知结果处理、重复请求、重启恢复、每账号 UA，以及仅 TUI 网络能力限制。

集成测试运行真实 HTTP 后端、API 客户端、SQLite、TUI 状态机和调度代码，只替换学校网络 transport，不发送真实预约。现场测试受 WFUSEAT_LIVE 显式开关控制，常规测试不要设置它。

Python 学习环境：

~~~~bash
uv sync --group dev
uv run python -m unittest discover -s tests
uv run jupyter lab
~~~~

Python submit 方法仍拒绝提交预约。Notebook 使用当前内核会话，不应把二维码、Cookie、签名或学校页面输出保存到共享文件。

## 发布前检查

仓库只保存源码、清空输出的 Notebook、测试与说明，不包含实际运行凭证和二进制。学校应用标识、公开 URL 和 OAuth client_id 属于公共配置，不是用户会话或客户端密钥。

~~~~bash
# 查看将要提交的文件，避免 git add -f 加入运行数据
git status --short
git add README.md .gitignore scripts internal cmd main.go

# 对 Git 暂存区执行检查；不会打印匹配到的凭证
python3 scripts/check_publication.py --staged
~~~~

检查会拒绝私钥、已识别的高风险 token 格式、个人绝对路径、敏感运行文件，以及带输出或附件的 Notebook。它是额外防线，不能替代人工复核；不要把“检查通过”理解为绝对不存在所有类型的秘密。

work/、dist/、数据库、日志、Cookie、会话文件和 Notebook 检查点均被忽略。运行数据留在本地，发布清理不会删除用户的真实账号或预订记录。

## 常见问题

### 获取二维码时提示 502

这通常表示后端无法完成学校登录请求，不是 TUI 本身生成二维码失败。后端默认直连，不继承终端环境代理。在需要代理的环境启动：

~~~~bash
./wfuseat-server --proxy http://127.0.0.1:7890
~~~~

这里的 127.0.0.1 是后端所在主机。更新后的错误会区分连接超时、学校 HTTP 错误与二维码格式异常，并避免暴露代理密码或原始凭证 URL。

### 扫码后回调失败

确认后端网络、代理与学校服务状态。程序不自动重复使用同一个授权码；重新生成二维码。学校拒绝、登录过期或风控原因不能通过单一状态码准确判断。

### 只看到一个大时间区间

先确认是否加载了正确教室和日期，及接口返回的 timeUnit。学校规定只能整段预约时，不会人为拆成多个小段。

### 客户端关闭后任务不执行

本地任务需要完整版或 --worker 持续运行。远程任务需要独立后端持续运行，客户端是否打开不影响调度。

### 改完代码，运行行为没有变化

需要重新编译并重启对应进程。替换文件不会更新已经运行的后端；重启前应查看当前任务状态，避免中断正在执行的请求。

### 换后端后看不到原账号

这是地址隔离的预期行为。每个后端有自己的身份和 Cookie 存储，需要在目标后端扫码；程序不自动把学校 Cookie 上传给另一个服务。

## 能力边界

- 依赖当前学校接口和页面结构，变化可能导致查询或签名失效。
- 不提供预约成功保证，也不提供通过 UA 或代理规避平台限制的保证。
- 不自动处理验证码、风控挑战或不明确的预约结果。
- 没有候补排队功能。
- 公网生产环境仍需要部署者配置证书、进程管理、限流、备份和日志权限。
- 未提供多节点调度与数据库集群方案。
- 学校凭证有自身有效期，后端 token 未过期不代表学校 Cookie 仍然有效。
