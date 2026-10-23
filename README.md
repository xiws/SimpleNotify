# SimpleNotify

一个轻量的邮件触发服务：轮询 IMAP 邮箱，收到匹配规则的邮件后执行配置的 bash 命令，并内置一个**只读**管理页面查看运行状态、规则和执行历史。

单二进制、无需前端构建、无外部 CDN 依赖。

## 特性

- **IMAP 轮询**：支持 TLS / STARTTLS / 明文连接；适配 163、QQ、Gmail 等邮箱的授权码登录；163 所需的 IMAP `ID` 扩展默认自动发送。
- **规则匹配**：按发件人、主题、正文做「包含」或「正则」匹配；按配置顺序取第一个命中的启用规则。
- **命令执行**：`bash -c` 执行；支持 Go 模板占位符与 `SN_*` 环境变量；超时后杀死整个进程组；输出自动截断。
- **可靠去重**：按邮箱 `UIDVALIDITY + UID` 持久化游标，不重复处理旧邮件；首次运行可选跳过历史邮件。
- **内嵌管理页**：概览、规则、执行历史与详情；每 3 秒自动刷新；默认仅监听 `127.0.0.1`，可选 Basic Auth。
- **只读 API**：管理页面和 HTTP 接口不提供任何写配置/手动执行能力。
- **热重载**：修改 `conf.json` 后发送 `SIGHUP` 即可应用规则、执行配置、邮箱设置和日志级别。

## 工作原理

```
┌────────────┐   IMAP 轮询   ┌──────────┐   匹配规则    ┌────────────┐
│  邮箱服务   │ ───────────> │  Poller  │ ───────────> │  Rule      │
└────────────┘              └────┬─────┘              │  Engine    │
                                 │                    └─────┬──────┘
                                 │ 解析邮件                  │ 渲染命令
                                 ▼                          ▼
                           ┌──────────┐               ┌────────────┐
                           │ UID 游标 │               │  bash -c   │
                           │ state.json│              └─────┬──────┘
                           └──────────┘                     │
                                                            ▼
                           ┌──────────┐               ┌────────────┐
                           │ 管理页面 │ <──────────── │  执行历史   │
                           └──────────┘               └────────────┘
```

单进程内 3 类长期任务：IMAP poller（串行处理邮件与命令）、HTTP 管理服务、主协程等待信号。

## 快速开始

要求 Go 1.24+。

```bash
# 1. 构建（产物名为 notify）
make build
# 或者：
go build -o notify ./cmd/notify

# 2. 准备配置
cp conf.example.json conf.json
# 编辑 conf.json，填入邮箱账号与授权码；推荐用环境变量传密码：
export SN_MAIL_PASSWORD='你的邮箱授权码'

# 3. 校验配置（不会联网）
./notify validate --config conf.json

# 4. 真实检查邮箱登录、SELECT 和服务器能力（替代临时脚本）
./notify validate --config conf.json --check-mail

# 5. 启动
./notify serve --config conf.json
# 打开 http://127.0.0.1:8899/
```

管理密码可以写在 `conf.json` 的 `server.password`，也可以用环境变量 `SN_ADMIN_PASSWORD` 提供。

### 邮箱授权码

| 邮箱 | IMAP 地址 | 说明 |
| --- | --- | --- |
| 163 | `imap.163.com:993` | 在网页版开启 IMAP，使用「客户端授权密码」；必须先发送 IMAP `ID`，本项目 `mail.send_id` 默认 `true` |
| QQ | `imap.qq.com:993` | 设置 → 账户 → 开启 IMAP，生成授权码 |
| Gmail | `imap.gmail.com:993` | 开启两步验证后生成「应用专用密码」 |

> 163 若返回 `Unsafe Login`，请确认 `send_id=true`、使用的是客户端授权码，并在网页版完成 IMAP 开启。

## 配置说明

配置为 JSON，**未知字段会直接报错**，便于发现拼写错误。完整示例见 [`conf.example.json`](conf.example.json)。

### server

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `addr` | `127.0.0.1:8899` | 管理页监听地址 |
| `password` | 空 | 设置后启用 HTTP Basic Auth；用户名任意 |
| `allow_insecure_remote` | `false` | 监听非回环地址但未设密码时，必须显式设为 `true` |

### log

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `level` | `info` | `debug` / `info` / `warn` / `error` |
| `file` | 空 | 追加写入的日志文件；为空时只输出到 stderr |

### mail

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `true` | 关闭后服务只提供管理页 |
| `host` | 空 | IMAP 主机 |
| `port` | `993`/`143` | 为 0 时按 `security` 推导 |
| `security` | `tls` | `tls` / `starttls` / `none` |
| `insecure_skip_verify` | `false` | 跳过 TLS 证书校验（仅自签证书时使用） |
| `username` | 空 | 完整邮箱地址 |
| `password` | 空 | 授权码；可改用 `SN_MAIL_PASSWORD` |
| `mailbox` | `INBOX` | 监听的文件夹 |
| `poll_interval` | `30s` | 最小 5s |
| `on_start` | `skip` | 无游标时的首次策略：`skip` / `unread` / `all` |
| `mark_as_seen` | `false` | 是否把处理过的邮件标记为已读 |
| `send_id` | `true` | 登录后发送 IMAP ID（163 必需） |
| `max_message_bytes` | `4194304` | 超过此大小的邮件直接跳过 |
| `max_body_bytes` | `262144` | 正文解析/匹配/历史中保留的最大字节数 |

### execute

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `shell` | `/bin/bash` | 执行器；不存在会启动失败 |
| `timeout` | `30s` | 单条命令超时，超时会杀死整个进程组 |
| `work_dir` | 配置文件目录 | 命令工作目录 |
| `max_output_bytes` | `65536` | stdout、stderr 各自保留上限 |
| `env` | `{}` | 追加给子进程的环境变量 |

### state / history

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `state.file` | `state.json` | UID 游标文件，原子写入，权限 0600 |
| `history.file` | 空 | 为空时历史仅存内存；设置后追加 JSONL |
| `history.memory` | `200` | 内存中保留的历史条数 |

相对路径统一相对于**配置文件所在目录**解析，因此从任何工作目录启动行为一致。

### rules

| 字段 | 说明 |
| --- | --- |
| `id` / `name` | 规则标识与显示名；`id` 为空时自动生成 `rule-N`，必须唯一 |
| `enabled` | 默认 `true` |
| `match` | 至少配置一个条件 |
| `command` | 通过配置的 shell 执行，支持模板 |

## 规则与命令

```json
{
  "rules": [
    {
      "id": "deploy",
      "name": "部署通知",
      "enabled": true,
      "match": {
        "from": "ci@example.com",
        "subject_contains": "deploy",
        "subject_regex": "",
        "body_contains": "",
        "body_regex": ""
      },
      "command": "echo \"部署邮件: {{.Subject}}\" >> /tmp/simple-notify.log"
    },
    {
      "id": "disk",
      "name": "磁盘告警",
      "match": {
        "body_regex": "disk usage.*(9[0-9]|100)%"
      },
      "command": "curl -fsS -X POST -d \"$SN_BODY\" http://127.0.0.1:9000/alert"
    }
  ]
}
```

**匹配语义**

- 所有非空条件按 **AND** 组合。
- `from` 对完整 From 头做大小写不敏感的子串匹配。
- `*_contains` 为大小写不敏感子串；`*_regex` 为 Go RE2 正则，默认区分大小写，可用 `(?i)` 关闭大小写敏感。
- 按配置顺序取**第一个**命中的启用规则；一条邮件只会触发一条规则。

**模板占位符**（`text/template`）

`{{.From}}`、`{{.To}}`、`{{.Subject}}`、`{{.Date}}`、`{{.MessageID}}`、`{{.UID}}`、`{{.Mailbox}}`、`{{.Body}}`、`{{.RuleID}}`、`{{.RuleName}}`

**环境变量**

`SN_FROM`、`SN_TO`、`SN_SUBJECT`、`SN_DATE`、`SN_MESSAGE_ID`、`SN_UID`、`SN_MAILBOX`、`SN_BODY`、`SN_RULE_ID`、`SN_RULE_NAME`

优先级：继承环境 < `execute.env` < `SN_*`。

> **安全提示**：邮件内容是外部不可信输入。把正文直接插入 shell 命令存在命令注入风险，推荐改用环境变量并正确引用，例如 `"$SN_BODY"`。

## 管理页面与 API

打开 `http://127.0.0.1:8899/`：

- **概览**：邮箱连接状态、轮询时间、UID 游标、执行配置、各类计数。
- **规则**：条件摘要、命令、最近一次执行结果。
- **执行历史**：时间、规则、邮件、状态、耗时、退出码；点击查看完整命令与 stdout/stderr。
- 页面每 3 秒刷新，切换标签/后台时自动暂停。

接口均为只读（非 GET 返回 405）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/healthz` | 健康检查，不需要登录 |
| GET | `/api/status` | 概览、规则、最近 10 条执行 |
| GET | `/api/executions?limit=&offset=` | 执行历史分页（最多 200 条/页） |
| GET | `/api/executions/{id}` | 单条执行详情 |
| GET | `/assets/*` | 内嵌静态资源 |

设置了 `server.password` 后，除 `/api/healthz` 外均需 Basic Auth。页面不含任何写配置接口，配置只能通过编辑 `conf.json` 生效。

## 运行机制

### 轮询与首次运行

1. 本地没有游标（或 `UIDVALIDITY` 变化）时按 `on_start` 处理：
   - `skip`（默认）：只记录当前 `UIDNEXT-1` 作为基线，不处理历史邮件；
   - `unread`：处理当前未读邮件；
   - `all`：处理邮箱内全部邮件（谨慎使用）。
2. 之后每次只处理 `last_uid+1 .. UIDNEXT-1` 的新邮件。
3. 每条邮件处理完成后立即落盘游标（即使命令失败也前进，避免失败邮件反复触发）。
4. 连接失败时指数退避重试（上限 5 分钟），管理页仍可用。

### 投递语义

命令执行成功但写游标前进程崩溃，重启后该邮件可能被再次处理，即**至少一次**语义。命令应尽量幂等。

### 热重载

```bash
kill -HUP $(pgrep -f 'notify serve')
```

- 立即生效：`rules`、`execute`、`mail`（会重启 poller）、`log.level`、`server.password`；
- 需要重启进程：`server.addr`、`state.file`、`history.*`、`log.file`。

配置无效时保留旧配置并记录错误，不会中断服务。

## 部署（systemd 示例）

```ini
[Unit]
Description=SimpleNotify
After=network-online.target
Wants=network-online.target

[Service]
User=notify
Group=notify
WorkingDirectory=/opt/simple-notify
ExecStart=/opt/simple-notify/notify serve --config /etc/simple-notify/conf.json
Environment=SN_MAIL_PASSWORD=your-app-password
Environment=SN_ADMIN_PASSWORD=your-admin-password
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

如需从公网访问管理页，请设置强密码并通过 Nginx/Caddy 等反向代理提供 HTTPS，不要直接暴露明文 HTTP。

## 安全建议

- 命令以服务运行用户权限执行，请使用专用低权限用户。
- `conf.json` 属于可信配置；邮件内容属于不可信输入。
- 密码优先通过 `SN_MAIL_PASSWORD` / `SN_ADMIN_PASSWORD` 注入，避免落盘；日志与 API 都会脱敏。
- 默认只监听回环地址。若监听公网且未设置密码，程序会拒绝启动（除非显式 `allow_insecure_remote=true`）。
- `state.json`、历史文件、日志文件权限为 0600，目录为 0700。
- **本仓库曾出现包含真实 163 邮箱授权码的 `test.py`。该文件已从 Git 索引移除并加入 `.gitignore`。请立即删除该文件并轮换授权码，任何凭据都不要提交到仓库。**

## 开发与测试

```bash
make test     # go test -race ./...
make vet      # go vet ./...
make fmt      # gofmt
make build
```

测试不需要真实邮箱或外网：`internal/mail` 使用 go-imap 的内存 IMAP 服务器（`imapserver/imapmemserver`）完成登录、取信、解析、规则执行和游标的端到端验证。

目录结构：

```
cmd/notify/          入口
internal/config/     配置加载、默认值、校验
internal/mail/       IMAP 客户端、MIME 解析、轮询器
internal/rules/      规则编译与匹配
internal/execute/    命令渲染与执行
internal/history/    执行历史（内存 + JSONL）
internal/state/      UID 游标
internal/web/        管理页与只读 API（go:embed）
internal/app/        组装、信号、优雅退出、热重载
internal/cli/        Cobra 命令
```

测试与构建：

```bash
go build ./...
go vet ./...
go test -race ./...
```

## 常见问题

**163 登录成功但 SELECT 报 `Unsafe Login`**  
确认 `mail.send_id=true`（默认），并使用客户端授权密码而非登录密码；必要时先在网页版完成 IMAP 开启。

**命令没有执行**  
依次检查：规则 `enabled`、`match` 条件、`notify validate` 输出、管理页「执行历史」和日志；首次运行默认 `on_start=skip`，不会处理启动前的邮件。

**重启后同一条邮件执行了两次**  
崩溃窗口导致，属于至少一次语义；请让命令幂等。

**历史记录很少**  
由 `history.memory` 控制；需要长期保存请配置 `history.file`。

**页面为什么不能编辑规则**  
按设计管理页只读；编辑 `conf.json` 后发送 `SIGHUP` 即可热加载。

**`notify send` 是什么**  
预留的主动发送命令，尚未实现。

## Roadmap

- `notify send`：主动发送通知（邮件/企业微信等）
- IMAP IDLE 实时推送
- 多账号 / 多文件夹
- 网页端规则编辑（会破坏当前只读设计，需重新评估鉴权与并发写）
- 执行失败重试与告警

## License

[MIT](LICENSE)
