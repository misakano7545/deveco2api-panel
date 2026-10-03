# deveco2api-panel

把华为 **DevEco Code** 的云端对话能力封装成 **OpenAI 兼容 API**（deveco2api 的 Go 版）。

- 单二进制、零运行时依赖（Go 标准库 + `github.com/BurntSushi/toml`）
- 与 Python 版 `config.toml` 完全兼容：同一账号、同一格式，可直接替换运行

## 构建

```bash
go build -o deveco2api-panel .
```

## 运行

```bash
./deveco2api-panel --config config.toml          # 默认 127.0.0.1:10102
./deveco2api-panel --port 10102 --no-browser
curl http://127.0.0.1:10102/v1/models -H "Authorization: Bearer <server.api_key>"
```

配置项与 Python 版一致，见 `config.example.toml`：
`deveco.keepalive_hours`（token 保活间隔，0=关）、`deveco.thinking_models`（流式思维链剥离清单）、
`server.api_key`（本地 API 密钥）。token 由登录流程自动写入 `[deveco.auth]`。

## 登录

### 本机登录（浏览器与服务器同机）

```bash
./deveco2api-panel --login
```

### 无头 / 远程服务器

**方式一：SSH 隧道（最简，推荐）**——服务器只打印 OAuth 地址：

```bash
# 1) 服务器
./deveco2api-panel --login --no-browser
# 2) 你的电脑（端口以第 1 步 URL 里 port= 为准；默认 10101，占用时回退 34567-34570）
ssh -L 10101:127.0.0.1:10101 <user>@<服务器>
# 3) 本机浏览器打开第 1 步打印的 URL 完成登录
```

**方式二：内置中继（不依赖 SSH 转发）**——本机同时运行「回调等待器 + 登录中继」：

```bash
./deveco2api-panel --login-relay --tunnel        # --tunnel：自动 cloudflared 快速隧道并打印外网地址
./deveco2api-panel --login-relay --relay-port 8788
```

浏览器打开提示地址（含口令 `?k=...`）完成登录，回调由中继转发回服务器、token 自动写入 `config.toml`；
页面显示「全部完成」即可关闭。可用参数：`--access-key`（固定口令）、`--timeout`（等待秒数，默认 600）。

## 测试

```bash
go test ./...    # 离线自测（mock 上游，无需华为账号）
```

## 与 Python 版的差异

功能对齐（协议/配置/行为一致），剪裁项：CDP 无头接力与 `/retry`、`/debug_complete`
调试端点未移植——Python 版在现行反代模式下这些路径已不触发。
