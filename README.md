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

默认直接连接，无需额外网络配置。

### 后端与仅 TUI

先启动后端：

~~~~bash
# 默认直连，监听本机 8787
./wfuseat-server --listen 127.0.0.1:8787

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

客户端填写后端服务的 URL，即可连接对应服务。

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

客户端连接 `https://seat.example.com:9443`。证书应被客户端系统信任，程序没有跳过证书校验的开关。

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

测试覆盖：扫码状态机、Cookie 恢复、多账号隔离、网络错误提示、时间区间、周计划、候选顺序、明确拒绝／未知结果处理、重复请求、重启恢复、每账号 UA，以及仅 TUI 网络能力限制。

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

这通常表示后端无法完成学校登录请求。请检查后端网络和学校服务状态；错误信息会区分连接超时、学校 HTTP 错误与二维码格式异常。特殊网络环境可显式使用 --proxy 配置，通常无需设置。

### 客户端连接失败

确认地址和端口正确。若已开启 VPN、TUN 或系统代理，可尝试关闭后重试；程序的直连设置不能绕过系统级网络接管。

### 扫码后回调失败

确认后端网络与学校服务状态。程序不自动重复使用同一个授权码；重新生成二维码。学校拒绝、登录过期或风控原因不能通过单一状态码准确判断。

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

### SSH 加密连接

完整客户端和仅 TUI 版本支持通过本机 OpenSSH 客户端建立加密隧道：

~~~bash
./wfuseat-tui --ssh wfuseat-access@your-server --ssh-key ~/.ssh/wfuseat_access
# 自定义 SSH 端口
./wfuseat-tui --ssh wfuseat-access@your-server --ssh-port 22 --ssh-key ~/.ssh/wfuseat_access
~~~

此模式通过 OpenSSH 直接连接服务器。首次使用前，应通过可信渠道核对服务器主机公钥并登记到 known_hosts；程序强制检查主机公钥，不自动接受陌生服务器。支持已解锁的 SSH agent；私钥需要口令时请先使用 ssh-add 解锁。

隧道只在客户端运行期间存在，连接服务器的 127.0.0.1:8787。后端仍按原方式运行，不需要开放这个端口。SSH 模式的登录凭证按 SSH 用户、主机和端口独立保存，不受临时本地端口变化影响；仍需扫码登录学校账号，不能凭 SSH 访问其他账号任务。--ssh 不能与 --server 混用。

服务端建议为每个使用者登记独立公钥，并配置专用隧道账号：
- 禁止密码认证、Shell、命令执行、SFTP、PTY 和 agent 转发。
- AllowTcpForwarding local，PermitOpen 127.0.0.1:8787，PermitListen none，MaxSessions 0。
- authorized_keys 由管理员维护；每个公钥使用 restrict,port-forwarding,permitopen="127.0.0.1:8787"。
- 不向其他用户分发管理员私钥或共用客户端私钥；撤销访问时删除对应公钥。

## 后端统一 Telegram 推送

后端统一汇总所有账号的预约执行结果到管理员指定的群。默认按**计划执行时间的同一分钟**分批，批次开始后等 **1 分钟**：22:15:00、22:15:40 的任务合为一批，在 22:16 后检查并推送（检查间隔 15 秒）。账号固定延迟不改变所属批次；如果仍有任务执行中，则继续等到任务完成或中断恢复后再汇总。

开启后取代该后端的逐账号日报，TUI 显示“后端统一配置”。任务 API 仍按账号隔离；仅管理员指定的群接收跨账号结果，账号展示姓与学号末四位。消息默认仅显示：

> **成功/全部：1/2**
>
> **失败：**
>
> • 李·0002：所选座位均被占用

下方有 **全部详情** 按钮，点击后在同一条 Telegram 消息中展开账号、日期、教室、时段、候选座位和失败原因；支持 **上一页／下一页／收起详情**。结果未知会明确标注“待核实”，不伪装成确定失败。失败很多时简报也会分页，每条简报均可展开全部详情。

独立后端和完整版 `--serve` 支持以下参数：

| 参数 | 用途 | 默认 |
| --- | --- | --- |
| `--telegram-bot-token` | Bot Token，也可用 `WFUSEAT_TELEGRAM_BOT_TOKEN` | 空，关闭统一推送 |
| `--telegram-token-file` | 从私有文件读取 Token；不能与 Token 参数同时指定 | 空 |
| `--telegram-chat-id` | 接收群或私聊 ID，也可用 `WFUSEAT_TELEGRAM_CHAT_ID` | 空 |
| `--telegram-proxy` | 仅 Telegram 使用的 HTTP(S) 代理，不影响学校请求 | 空，直连 |
| `--telegram-name` | 展开详情后的标题与称呼 | notice |
| `--telegram-delay` | 从批次开始到汇总的等待时间 | 1m |
| `--telegram-batch-window` | 固定分批窗口，1–60 整数分钟 | 1m |

```sh
./wfuseat-server-linux-amd64 \
  --telegram-token-file /etc/wfuseat/telegram.token \
  --telegram-chat-id=-1001234567890 \
  --telegram-name "晚安自习室" \
  --telegram-delay 1m
```

示例 ID 是占位值，替换为实际群 ID。Token 文件应仅服务用户可读。也可使用上述环境变量；环境中的 Token 不显示在帮助或应用日志中。启动参数不写回账号配置，重启需保留参数或服务环境文件。延迟不可短于分批窗口；配置无效会拒绝启动。

### 与现有学习机器人共用 Bot

Telegram 同一个 Bot 应只有一个更新监听者。预约后端**不调用 getUpdates，也不设置 Webhook**。它只发送消息，并接收现有学习机器人转发的按钮事件。

仓库提供 `integrations/wfuseat_callback.py`。将此模块放入学习机器人项目，在其现有更新分发处、聊天白名单及学习按钮解析之前加入：

```python
from wfuseat_callback import forward_wfuseat_callback

# update 为 Telegram 原始更新字典；bot_token 为学习机器人已有的 Token。
if forward_wfuseat_callback(update, bot_token, "http://127.0.0.1:8787"):
    continue  # 当前位于更新循环；若在单条消息处理函数中，改为 return
# 后面继续原来的学习机器人逻辑。
```

仅 `wfuseat:` 按钮会被转发，普通消息及原有学习按钮保持原流程。转发到 `POST /v1/telegram/callback`，双方用同一 Bot Token 派生鉴权密钥；接口检查接收群和详情有效期，Token 本身不通过转发请求发送。默认走同机回环地址，不读取系统代理；远程后端须使用 HTTPS。适配器不安装第二个监听器，不改变现有机器人的消息确认逻辑。尚未安装这个适配器时，消息可以送达，但按钮无法展开。

详情保存七天后拒绝访问；按钮不携带学校凭证。批次在汇总时保存固定快照，后续状态变化不追加消息。服务重启最多补发最近七天未投递批次，每页独立防重复；明确限流才等待重试，未知投递结果需手动核实。日志记录批次、页码和投递结果。发送端默认直连 Telegram，需确保后端网络可达。

## 逐账号 Telegram 每日回信（未启用后端统一模式时）

在账号面板按右方向键进入右侧详情，配置 **Telegram 推送、Bot Token、Chat ID、推送称呼**，按 **Ctrl+S** 保存。上下移动选择，Enter 编辑文本，空格切换开关。

1. 在 Telegram 的 [@BotFather](https://t.me/BotFather) 创建机器人，取得 Bot Token。
2. 向机器人发送 `/start`，确认它可以给你发私信；填写自己的数字 Chat ID。也支持已加入机器人的群组，群组 ID 通常为负数。
3. 填写一个喜欢的称呼，例如“小林”，开启推送并保存。Token 在界面中隐藏；编辑时留空保留原值，输入 `-` 清除（先关闭推送）。

配置按学校账号隔离。远程模式下 Token 只保存在对应服务端账号的私有配置文件中，接口仅返回“已配置”标记，客户端不持久化 Bot Token。完整版本的本地模式将 Token 保存在账号自己的配置文件中；配置打印会隐藏它。删除该账号也会删除其配置。不要提交或分享运行数据目录。

**推送时机：** 按北京时间的“执行日”汇总。当当天已知的任务全部结束并稳定一分钟后，后台发送一条消息；消息中另外注明实际预约日期，所以今晚预约明天的座位不会混淆。循环任务使用独立的执行快照，推进到下一天不会覆盖结果。

- 区分成功、未成功、需要重新扫码、错过执行、暂停和结果未知；包含教室、时段、候选座位顺序及个性化称呼。
- 没有执行记录、也没有到期等待任务的日期不打扰。有到期任务始终未执行，跨日后汇总为“尚未执行”，不声称学校拒绝了预约。
- 每账号每执行日最多一份汇总；当天汇总发出后新增的任务不会追加第二份日报。最多展示八项，完整详情在 TUI 中查看。
- 重启后补发最近七天尚未发送的汇总；升级前的旧自由文本日志不追溯解析为通知。
- 推送独立于预约执行，关闭 TUI 后独立后端仍可发送；本地模式需要完整程序或 worker 持续运行。
- 发送前持久化占位，避免多进程或重启重复推送。Telegram 明确返回限流时按要求等待重试；连接超时、响应不明或发送过程中崩溃时不盲目重发，需在聊天记录核实。这是防重复策略，不能保证绝不漏报。
- 账号详情显示最近的投递状态。Token 无效、未 `/start`、Chat ID 错误等会显示可操作的提示。修正配置后用于之后的日报，已经占位的日报不会自动重新发送。

消息示例：

> 📚 **自习座位 · 每日回信**<br>
> 2026-09-26 · 北京时间<br>
>
> 小林，今天的预约进展来啦。<br>
> ✅ 成功 1 · 🟠 未成功 0 · ❔ 待核实 0<br>
>
> **✅ 预约成功**<br>
> 📍 示例自习室<br>
> 🗓 2026-09-27  08:00–10:00<br>
> 🪑 候选顺序：001 → 002<br>
>
> 座位准备好了，带上书和好心情出发吧。☀️<br>

发送端使用 [Telegram Bot API](https://core.telegram.org/bots/api#sendmessage) 的 HTTPS 接口，默认直连，运行后端的机器需要能够访问 `api.telegram.org`。不沿用学校请求代理，也不读取系统代理环境变量。自动化测试替换学校与 Telegram 的传输层，不会真实预约或发送聊天消息。

## 免责声明

本项目仅供学习、研究和技术交流使用，与学校及相关平台无隶属或官方合作关系。

请仅在获得授权的范围内使用，并遵守学校及平台的使用规则。请勿用于未授权访问、干扰服务、恶意占用资源或其他不当用途。使用者应自行保护账号凭证，并对自己的操作负责。

本项目按现状提供，不保证接口持续可用、预约成功或数据绝对安全。使用前请了解相关功能及风险，重要操作请自行核实。
