# OpenCode Go Provider（内置）

内置的 OpenCode **Go 套餐**接入：把任意 harness 的请求转发到 `https://opencode.ai/zen/go/v1/*`，并让上游始终看到 OpenCode CLI 的请求指纹与会话语义。

## 1. 能力边界

| 项 | 支持情况 |
|---|---|
| OpenCode Go 套餐（付费） | ✅ 支持 |
| OpenCode Zen 付费与免费匿名模型 | ❌ 不支持（Zen 付费用 `openai-compatibility` 另行配置） |
| 入站协议 | `/v1/chat/completions`、`/v1/messages`、`/v1/responses` |
| 上游协议 | 优先使用入站协议端点；仅在端点明确不可用时转换 |
| 多账户 | ✅ 支持，额度耗尽自动跳过 |
| 额度查询 | ✅ `GET /zen/go/v1/usage`（仅需 API Key） |
| 控制台 API（会话 cookie / 账单明细 / 自动建撤 key） | ❌ 不支持 |
| 代理池 / 出口 IP 轮换 | ❌ 不支持；仅 `proxy-url` 或直连 |

## 2. 配置

### 2.1 客户端访问 Key（谁可以调用 CPA）

```yaml
access:
  api-keys:
    - "your-client-key"
```

### 2.2 上游账户（每个账户一个 auth 文件）

在 `oauth.auth-dir`（默认 `~/.cli-proxy-api`）下放置：

```json
{
  "type": "opencode",
  "api_key": "oc_sk_xxxxxxxx",
  "label": "acc1"
}
```

- `api_key` 来自 <https://opencode.ai/console/account> 的 **Personal API keys**。
- `label` 仅用于展示。
- 多个文件即多个账户；文件名决定 auth ID。

### 2.3 可选网络与覆盖项

```yaml
requests:
  proxy-url: "http://127.0.0.1:7897"   # 或 "direct"
```

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `OPENCODE_BASE_URL` | `https://opencode.ai` | 上游基地址 |
| `OPENCODE_CAPABILITIES_URL` | `https://models.opencode.ai/api.json` | 能力目录 |
| `OPENCODE_PROTOCOLS` | 空 | 强制模型协议，如 `gpt-x=responses,claude-y=anthropic` |
| `OPENCODE_MODEL_REFRESH_INTERVAL` | `6h` | 模型目录刷新间隔 |
| `OPENCODE_QUOTA_INTERVAL` | `5m` | 额度查询间隔 |

## 3. 请求流程

```
客户端 → CPA 鉴权
      → 模型 → Go 套餐解析，优先尝试入站协议端点
      → 端点明确不可用时，执行器才转换为可用后备协议（CPA 不会预先转换）
      → 注入 OpenCode 指纹与会话头 → POST /zen/go/v1/{chat/completions|responses|messages}
      → 响应（流式/非流式）再翻回入站协议
```

> **重要**：CLIProxyAPI 不会把请求体预先翻译成本执行器声明的格式，`req.Payload` 仍是客户端原始协议。因此协议转换由 `internal/opencode` 自己完成（`sdktranslator.TranslateRequest` / `TranslateStream` / `TranslateNonStream`），这也是跨协议能正确工作的前提。

### 3.1 指纹头（与 GoProxy 实现一致）

| 头 | 值 |
|---|---|
| `User-Agent` | `opencode/latest/<version>/cli` |
| `x-opencode-client` | `cli` |
| `x-opencode-session` / `x-opencode-session-id` / `x-session-affinity` / `X-Session-Id` | 归一化会话 ID（`ses_` + 12 位降序时间 + 14 位 base62） |
| `x-opencode-project` | 由会话推导的 40 位十六进制 |
| `x-parent-session-id` | 存在父会话时 |
| `Accept` | 流式 `text/event-stream`，非流 `application/json` |
| 鉴权 | OpenAI 协议 `Authorization: Bearer`；Anthropic `x-api-key` |

客户端带来的 `x-opencode-request`、旧 `x-opencode-parent-session-id` 会被剥离。

会话ID来源优先级：CPA 元数据（canonical / derived / execution session）→ 入站 `X-Session-Id` / `X-Client-Request-Id`。合法 `ses_` 形态且时间前缀降序的 ID 原样保留，其余按 SHA-256 确定性重编码，保证多轮稳定、不同 harness 归一。

### 3.2 端点自适应（模型换端点）

执行器首先尝试客户端入站协议对应的端点，不因目录声明了其它默认协议而提前转换。仅在上游明确返回端点不可用（例如 `422 Endpoint is unavailable`）时，才尝试已记忆的成功协议或目录默认协议，再按 chat → responses → anthropic 补齐其它候选。已确认不可用的模型/协议组合会在进程内记忆并跳过。鉴权错误、模型错误及 `response_format` 等参数校验错误不会触发换端点；进程重启后重新探测。

### 3.3 协议判定优先级

执行端点优先级：入站协议 → 已记忆成功协议（无则取目录默认协议）→ chat / responses / anthropic 中尚未尝试的协议。所有步骤均跳过已确认不可用的组合，且只在前一端点明确不可用时继续。

目录默认协议仅作为转换后备，来源优先级如下：

| 顺序 | 来源 |
|---|---|
| 1 | 本地 `OPENCODE_PROTOCOLS` 覆盖 |
| 2 | 能力目录 `models.opencode.ai/api.json` 中 `opencode-go` 条目的 `provider.npm` |
| 3 | `GET /zen/go/v1/models`（未在目录中的模型按 `chat` 兜底） |

SDK 到协议的映射：`@ai-sdk/openai` 或以 `/openai` 结尾 → `responses`；含 `anthropic` → `messages`；含 `openai-compatible` → `chat`；其它 → 视为不可用，不猜测。

### 3.4 流式封装边界

- 交叉协议转换器接收带 `data:` 前缀的 SSE 数据行，不能传剥掉前缀的裸 JSON；多行 JSON 数据复用现有 SSE 扫描器合并后转换。
- 原生 Chat 的执行器输出只含 JSON；`data:`、空行和最终 `[DONE]` 由 HTTP handler 添加，不能重复封装。
- 原生 Messages / Responses 保留完整 SSE 的 `event:`、`data:`、注释和空行；HTTP handler 直接消费流式帧，不能只转发数据内容。
- 定向验证：`powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/windows/verify-opencode-stream.ps1`；`-Focus` 仅跑封装回归，`-Race` 对整个 OpenCode provider 包运行竞态检查。

## 4. 额度与管理

### 4.1 额度接口

```
GET https://opencode.ai/zen/go/v1/usage
Authorization: Bearer <api_key>
```

```json
{"usage":{
  "rolling":{"status":"ok","percent":12.5,"resetsAt":"2026-10-20T16:00:37Z"},
  "weekly":{"status":"ok","percent":30,"resetsAt":"2026-10-26T00:00:00Z"},
  "monthly":{"status":"ok","percent":45,"resetsAt":"2026-11-20T10:53:30Z"}}}
```

不需要控制台 cookie。

### 4.2 模型暴露管理

只暴露需要的模型：控制台「OAuth 登录」页的 OpenCode 卡片提供逐模型开关。

```bash
# 查询目录与当前被排除的模型
curl -H "Authorization: Bearer <mgmt-key>" http://127.0.0.1:8317/v0/management/opencode/models

# 设置排除列表（写入 oauth.excluded-models.opencode 并立即重新发布模型）
curl -X PUT -H "Authorization: Bearer <mgmt-key>" -H "Content-Type: application/json" \
     -d '{"excluded":["minimax-m3.1-flash-free"]}' \
     http://127.0.0.1:8317/v0/management/opencode/models
```

说明：

- 排除后模型从 `/v1/models` 消失，且不再参与路由；`/v0/management/opencode/models` 仍会返回完整目录（供控制台展示）。
- PUT 会即时重新发布模型，无需重启；重启后从配置恢复。
- 配置落在 `oauth.excluded-models.opencode`，与通用 excluded-models 语义一致（支持 `*` 通配）。

### 4.3 管理接口

```
GET /v0/management/opencode/accounts
Authorization: Bearer <management.secret-key>
```

返回（`api_key` 仅回掩码）：

```json
{"accounts":[{
  "id":"opencode-go-1.json","label":"acc1","provider":"opencode",
  "status":"active","disabled":false,"api_key_hint":"oc_sk_…****",
  "quota":{"rolling":{"status":"ok","percent":12.5,"resets_at":"…"},
           "weekly":{...},"monthly":{...},"exhausted":false,"fetched_at":"…"}
}]}
```

### 4.4 账户选择

- `disabled` 或状态为 `disabled` 的账户跳过；
- 任一一档 `percent ≥ 100` 或 `status != ok` 视为已耗尽，跳过；
- 其余按 `100 - 最高档百分比`（剩余空间）择大，实现简单加权轮询。

## 5. 用量统计与请求明细

OpenCode 与其他内置 provider 一样进入统一用量统计（`usage.Manager` → Redis 队列 → 面板 `usage_events`）：

| 事件 | 上报方式 |
|---|---|
| 非流式成功 | 解析响应 `usage` 后 `Publish` |
| 流式成功（上游带 usage） | `StreamUsageBuffer` 观察最后一帧 |
| 流式成功（上游不带 usage） | 结束时 `EnsurePublished` 记 1 条请求 |
| 上游错误 / 读流失败 | `TrackFailure` / `PublishFailure`，带状态码 |
| 客户端取消 | `PublishFailure`（ctx 取消） |

chat 协议流式会自动在请求体注入 `stream_options.include_usage=true`，否则上游不回 token 数（客户端显式传 `false` 也会被覆盖）。

请求明细字段：

| 字段 | 来源 |
|---|---|
| provider | `opencode` |
| auth_id | auth 文件名 |
| session_id / parent_session_id | 归一化后的 OpenCode 会话 ID（与实际发给上游的 `x-opencode-session` 一致） |
| response_model | 上游响应里声明的实际模型 |
| stream / ttft / latency | 实际链路测量（`TrackHTTPClient`） |
| failed / status_code | 失败请求 |

面板用量页的 provider 下拉是动态 `select distinct provider`，因此无需改动即可看到 `opencode`。

## 6. 已知限制

| 项 | 说明 |
|---|---|
| 模型注册时机 | 执行器在构造时同步加载目录，并按 auth-dir 中的 `type: opencode` 文件自行绑定模型；CPA 自身的按 auth 注册发生在延后的一轮，若不自行绑定，启动后数秒内请求会返回 `auth_not_found` |
| 额度展示位置 | 「配额管理」页统一展示三档额度；OpenCode 卡片只做账户与模型管理 |
| 额度不是 CPA 内置调度状态 | 仅在账户选择时读取本地快照，不写入 CPA `QuotaState`；快照超过 1 分钟会在管理接口调用时刷新 |
| 无被动额度更新 | 只有管理接口或首次请求触发的刷新，不做后台定时轮询 |
| 会话不持久化 | 进程重启后会话 ID 由入站信号重新推导；下游未提供稳定会话信号时会话可能变化 |
| 仅 Go 套餐模型 | 目录只取 `opencode-go` 条目，Zen/匿名模型不会出现 |
