# 目录约定

代码结构对齐 `workbuddy2api-panel`：入口在 `cmd/`，能力在 `internal/<域>/`，测试与被测包同目录。

```
cmd/server/          网关二进制 deveco2api-panel（main.go 入口 + wiring.go 装配）
cmd/login/           登录二进制 deveco2api-login（交互登录 / --relay 中继登录）
internal/auth/       华为登录、回调收尾、token 刷新（Store 是唯一持有 token 的地方）
internal/config/     config.json 读写（两个二进制共用；字段名与 Python 版 config.toml 同名）
internal/httpauth/   Bearer 鉴权原语（常量时间比较，网关与面板同口径）
internal/jsonval/    上游 JSON 取值 / 序列化 helper（nil 安全、不转义 HTML）
internal/logfmt/     日志行格式 [HH:MM:SS][LEVEL] msg + 面板日志 sink
internal/panel/      内嵌控制台 index.html / app.js / index.go / panel.go / ring.go
internal/relay/      无头登录中继（反代华为登录页、改写回调、cloudflared 隧道）
internal/server/     网关 HTTP 表面 /v1/*（并挂载 /panel/）
internal/session/    会话 / 消息 ID 生成（对齐官方客户端 id.ts）
internal/upstream/   上游客户端：请求体构造、SSE 转发、思维链剥离、错误转译
```

## 硬约束

- **与 Python 版（`deveco2api`）行为对齐**：协议路径、配置字段名、日志口径、状态码映射
  （限流 429 / 上游 4xx 沿用 / 其余 502）必须一致，两版可直接互换运行。
- **internal 包不认识 config.json**：依赖由 `cmd` 注入（`cmd/server/wiring.go`、`cmd/login`）。
  这样包的测试不需要配置文件，也不会因为配置项改动而连锁改包。
- **出站只走 `internal/upstream`，登录只走 `internal/auth`**：`internal/server` 与
  `internal/panel` 不直接发华为请求。
- **面板只读**：概览/模型/日志三个视图；登录、改配置等写操作走 CLI，不在页面里做。
- **日志一律 `logfmt.Infof/Warnf/Errorf`**：stdout 是持久出口，面板环形缓冲只是观测窗口
  （进程重启即空，不落盘）。

## 测试

```bash
go test ./...              # 全部离线（mock 上游，无需华为账号）
go test ./internal/relay/  # 单包
```

非平凡逻辑都留一个可跑的检查：`internal/server/handler_test.go` 是主链路（鉴权、刷新重试、
思维链剥离、限流转译、保活），`internal/panel/ring_test.go` 管环形缓冲，`internal/session`
管 ID 格式，`internal/auth` 管登录收尾。

## 配置与凭证

`config.json` 不入库（见 `.gitignore`），模板见 `config.example.json`。凭证写在 `deveco.auth`，
由登录流程自动落盘（`config.SaveTokens`：MarshalIndent → tmp（0600，文件含 token）→ rename）。
字段名与 Python 版 `config.toml` 一一对应（`server.*` / `deveco.*` / `logging.*`），只是容器是 JSON，
两版不再共用同一份文件——转换时把 section.key 拍平成同名 JSON 键即可。
