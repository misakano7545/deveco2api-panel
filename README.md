# deveco2api-panel

把华为 **DevEco Code** 的云端对话能力封装成 **OpenAI 兼容 API**（deveco2api 的 Go 版），
自带内嵌 Web 控制台。

- 单二进制、零运行时依赖（Go 标准库 + `github.com/BurntSushi/toml`）
- 配置文件 `config.json`（字段名与 Python 版 `config.toml` 一一对应，容器不同）
- 目录结构对齐 `workbuddy2api-panel`：`cmd/` 放入口，`internal/<域>/` 放能力

## 目录结构

```
cmd/server/          网关 deveco2api-panel（入口 + wiring 装配）
cmd/login/           登录工具 deveco2api-login（交互登录 / --relay 中继）
internal/auth/       华为登录、回调收尾、token 刷新
internal/config/     config.json 读写（两个二进制共用）
internal/httpauth/   Bearer 鉴权原语（常量时间，网关与面板同口径）
internal/jsonval/    JSON 取值/序列化 helper
internal/logfmt/     日志行格式 + 面板日志 sink
internal/panel/      内嵌控制台（index.html / app.js / index.go / panel.go / ring.go）
internal/relay/      无头登录中继
internal/server/     网关 HTTP 表面 /v1/*（挂载 /panel/）
internal/session/    会话/消息 ID 生成
internal/upstream/   上游客户端：请求体构造、SSE 转发、思维链剥离、错误转译
```

约定与硬约束见 `AGENTS.md`。

## 构建

```bash
go build -o deveco2api-panel ./cmd/server
go build -o deveco2api-login ./cmd/login
```

## 运行

```bash
./deveco2api-panel --config config.json          # 默认 127.0.0.1:10102
./deveco2api-panel --port 10103 --no-browser     # 临时换端口
```

| 端点 | 说明 |
|---|---|
| `POST /v1/chat/completions` | OpenAI 兼容对话；`stream: true` 走 SSE |
| `GET /v1/models` | 当前账号可见模型 |
| `GET /panel/` | Web 控制台（概览 / 模型 / 日志） |
| `GET /health` | 存活探针 |

```bash
curl http://127.0.0.1:10102/v1/models -H "Authorization: Bearer <server.api_key>"
```

控制台与 `/v1/*` 共用 `server.api_key`（Bearer）；`api_key` 为空 = 不鉴权（仅本机/私网用法）。
面板页面本身不含任何密钥，密钥只发给 `/panel/api/*`。

配置项与 Python 版一一对应，见 `config.example.json`：
`deveco.keepalive_hours`（token 保活间隔，0=关）、`deveco.thinking_models`（流式思维链剥离清单）、
`server.api_key`（本地 API 密钥）。token 由登录流程自动写入 `deveco.auth`。

## 登录

登录是一次性动作，走 `deveco2api-login`；网关启动时也可用 `./deveco2api-panel --login`
顺带确保 token 有效（过期则交互登录）。

### 本机登录（浏览器与服务器同机）

```bash
./deveco2api-login
```

### 无头 / 远程服务器：SSH 隧道（最简，推荐）

服务器只打印 OAuth 地址：

```bash
# 1) 服务器
./deveco2api-login --no-browser
# 2) 你的电脑（端口以第 1 步 URL 里 port= 为准；默认 10101，占用时回退 34567-34570）
ssh -L 10101:127.0.0.1:10101 <user>@<服务器>
# 3) 本机浏览器打开第 1 步打印的 URL 完成登录
```

### 无头 / 远程服务器：内置中继（不依赖 SSH 转发）

本机同时运行「回调等待器 + 登录中继」：

```bash
./deveco2api-login --relay --tunnel        # --tunnel：自动 cloudflared 快速隧道并打印外网地址
./deveco2api-login --relay --relay-port 8788
```

浏览器打开提示地址（含口令 `?k=...`）完成登录，回调由中继转发回服务器、token 自动写入
`config.json`；页面显示「全部完成」即可关闭。可用参数：`--access-key`（固定口令）、
`--timeout`（等待秒数，默认 600）。

## 测试

```bash
go test ./...    # 全部离线（mock 上游，无需华为账号）
```

## 与 Python 版的差异

功能对齐（协议/配置/行为一致），剪裁项：CDP 无头接力与 `/retry`、`/debug_complete`
调试端点未移植——Python 版在现行反代模式下这些路径已不触发。
