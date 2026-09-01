# enterprise-access-audit 插件配置与使用说明

`enterprise-access-audit` 是 CLIProxyAPI 的动态库插件，用于：

- 按 Enterprise Key 配置禁止访问的模型；
- 记录标准文本请求的人工审计信息；
- 对明确由上游返回 `cyber_policy` 的请求进行安全信号标记；
- 保存按 API Key Hash 和 UTC 日期分区的 JSONL 审计文件；
- 通过 Management Center 的插件资源页面查看审计记录、访问策略和插件设置。

插件只在请求执行阶段拒绝模型访问，不过滤模型目录，不自动封禁用户，也不自动进行内容风险判定。插件是 CPA 进程内动态库，只应安装来源可信且已校验版本的文件。

## 1. 选择正确的 CPA 发布包

插件必须与 CPA 的操作系统和 CPU 架构匹配。优先使用同一个版本号的 CPA 发布包，不要从其他版本单独复制插件。

| 操作系统 | amd64 发布包 | arm64 发布包 | 动态库文件 | 插件目录 |
|---|---|---|---|---|
| Windows | `windows_amd64` | `windows_aarch64` | `.dll` | `plugins/windows/amd64/` 或 `plugins/windows/arm64/` |
| macOS | `darwin_amd64` | `darwin_aarch64` | `.dylib` | `plugins/darwin/amd64/` 或 `plugins/darwin/arm64/` |
| Linux glibc | `linux_amd64` | `linux_aarch64` | `.so` | `plugins/linux/amd64/` 或 `plugins/linux/arm64/` |

发布包中的插件文件应当位于对应的 `plugins/<GOOS>/<GOARCH>/` 目录。例如：

```text
plugins/
├── windows/amd64/enterprise-access-audit.dll
├── darwin/arm64/enterprise-access-audit.dylib
└── linux/amd64/enterprise-access-audit.so
```

实际运行时只保留当前 CPA 所需的平台文件即可。插件加载器也支持将动态库直接放在 `plugins/` 根目录，但不建议混放多个平台或多个架构的同名文件。

### 不支持插件的发布包

以下发布包不包含或不支持动态库插件：

- Linux `*_no-plugin` 包；
- FreeBSD 包；
- 其他未明确标记为插件兼容的可移植构建。

Linux 需要选择普通的 glibc 包，而不是 `no-plugin` 包。普通 Linux 发布包和插件使用 GLIBC 2.17 基线构建；目标机器仍必须满足动态库加载条件。

## 2. 安装动态库

### 2.1 使用 CPA 发布包

1. 下载与 CPA 版本相同的插件兼容发布包。
2. 将压缩包完整解压到 CPA 的运行目录。
3. 确认动态库文件名和目录正确：
   - Windows：`plugins/windows/<架构>/enterprise-access-audit.dll`
   - macOS：`plugins/darwin/<架构>/enterprise-access-audit.dylib`
   - Linux：`plugins/linux/<架构>/enterprise-access-audit.so`
4. 确保 CPA 的工作目录、插件目录和插件数据目录对运行 CPA 的账户可读写。
5. 修改 `config.yaml`，启用动态插件和本插件。
6. 重启 CPA，或通过 CPA 支持的配置重载机制重新加载插件。

`enterprise-access-audit` 是插件 ID，动态库的基础文件名必须保持为 `enterprise-access-audit`。不要把 API Key、用户名、邮箱或请求 ID 放进文件名。

### 2.2 手动复制插件

如果使用单独下载的动态库，默认插件根目录是 CPA 工作目录下的 `plugins`。也可以在配置中指定其他目录：

```yaml
plugins:
  enabled: true
  dir: "/opt/cliproxyapi/plugins"
```

Windows 示例：

```yaml
plugins:
  enabled: true
  dir: "plugins"
```

将对应平台的动态库复制到以下任一位置：

```text
<插件根目录>/<GOOS>/<GOARCH>/enterprise-access-audit.<ext>
<插件根目录>/enterprise-access-audit.<ext>
```

平台目录优先。不要同时在平台目录和根目录放置不同版本的同名插件，避免加载到不期望的文件。

## 3. 最小配置

在 CPA 的 `config.yaml` 中加入：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    enterprise-access-audit:
      enabled: true
      priority: 100
```

注意：

- `plugins.enabled` 是全局开关，必须为 `true`；
- `configs.enterprise-access-audit.enabled` 是本插件开关，也必须为 `true`；
- 只设置单个插件的 `enabled: true` 不会自动打开全局插件系统；
- `priority` 使用 `100` 即可，不要随意与其他拦截插件使用相同优先级；
- 配置键必须是精确的 `enterprise-access-audit`。

配置修改后，使用与平时相同的方式启动 CPA，例如：

```bash
./cli-proxy-api --config ./config.yaml
```

Windows PowerShell：

```powershell
.\cli-proxy-api.exe --config .\config.yaml
```

## 4. 完整配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    enterprise-access-audit:
      enabled: true
      priority: 100
      data_dir: ".cli-proxy-api/plugins/enterprise-access-audit"
      database_path: ".cli-proxy-api/plugins/enterprise-access-audit/enterprise-access-audit.sqlite"
      retention_days: 30
      default_audit_enabled: true
      max_text_bytes: 32768
      cleanup_interval_seconds: 3600
```

配置字段如下：

| 字段 | 默认值 | 有效范围 | 说明 |
|---|---:|---:|---|
| `enabled` | `false` | — | 本插件开关，由 CPA 插件主配置读取。 |
| `priority` | `0` | — | 插件拦截优先级；建议使用 `100`。 |
| `data_dir` | `.cli-proxy-api/plugins/enterprise-access-audit` | — | JSONL、SQLite 及相关数据目录。相对路径以 CPA 工作目录为基准。 |
| `database_path` | `<data_dir>/enterprise-access-audit.sqlite` | — | SQLite 路径。只保存策略、设置和迁移记录，不保存审计正文。 |
| `retention_days` | `30` | `1–3650` | 审计记录保留天数。启动时和定时清理时执行。 |
| `default_audit_enabled` | `true` | — | 没有单独策略记录的 Enterprise Key 使用的默认审计开关。 |
| `max_text_bytes` | `32768` | `1–1048576` | 单条用户文本最大保存字节数，超出时保留截断标记。 |
| `cleanup_interval_seconds` | `3600` | `1–86400` | 过期记录清理间隔。 |

路径规则：

- 相对 `data_dir` 和 `database_path` 都以 CPA 当前工作目录为基准，不以 `config.yaml` 文件所在目录为基准；
- 插件会自动创建数据目录及数据库父目录；
- 数据目录建议放在 CPA 的受保护数据目录中，不要放在临时目录或公开 Web 目录中；
- Windows 配置中的路径可以使用 `/`，也可以使用转义正确的 `\\`。

## 5. 三个平台的配置示例

三种操作系统使用相同的 YAML 字段，主要差异是动态库后缀、CPA 二进制和平台目录。

### Windows

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    enterprise-access-audit:
      enabled: true
      priority: 100
      data_dir: ".cli-proxy-api/plugins/enterprise-access-audit"
      database_path: ".cli-proxy-api/plugins/enterprise-access-audit/enterprise-access-audit.sqlite"
      retention_days: 30
      default_audit_enabled: true
      max_text_bytes: 32768
      cleanup_interval_seconds: 3600
```

动态库：

```text
plugins/windows/amd64/enterprise-access-audit.dll
plugins/windows/arm64/enterprise-access-audit.dll
```

确认 CPA 与 DLL 架构一致。Windows ARM64 必须使用 ARM64 DLL；不要用 amd64 DLL 代替。更新正在运行的插件前必须先停止 CPA，因为动态库会被进程加载并锁定；生产环境建议先将新文件放到临时名称，停止 CPA 后再完成替换和重启。

### macOS

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    enterprise-access-audit:
      enabled: true
      priority: 100
      data_dir: ".cli-proxy-api/plugins/enterprise-access-audit"
      database_path: ".cli-proxy-api/plugins/enterprise-access-audit/enterprise-access-audit.sqlite"
      retention_days: 30
      default_audit_enabled: true
      max_text_bytes: 32768
      cleanup_interval_seconds: 3600
```

动态库：

```text
plugins/darwin/amd64/enterprise-access-audit.dylib
plugins/darwin/arm64/enterprise-access-audit.dylib
```

Apple Silicon 使用 arm64 包，Intel Mac 使用 amd64 包。若系统的安全策略阻止来自网络的动态库加载，应先核对 Release 校验和及来源，再按照组织的 macOS 软件信任流程处理，不要在未验证文件来源的情况下放宽系统安全策略。

### Linux

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    enterprise-access-audit:
      enabled: true
      priority: 100
      data_dir: ".cli-proxy-api/plugins/enterprise-access-audit"
      database_path: ".cli-proxy-api/plugins/enterprise-access-audit/enterprise-access-audit.sqlite"
      retention_days: 30
      default_audit_enabled: true
      max_text_bytes: 32768
      cleanup_interval_seconds: 3600
```

动态库：

```text
plugins/linux/amd64/enterprise-access-audit.so
plugins/linux/arm64/enterprise-access-audit.so
```

请选择普通 Linux glibc 发布包。`*_no-plugin` 包是无动态插件的可移植版本，即使手动复制 `.so` 也不会加载。Linux 服务器上可以使用以下命令检查主程序、架构和动态库：

```bash
file ./cli-proxy-api
file ./plugins/linux/amd64/enterprise-access-audit.so
ldd --version
```

如果使用容器或精简发行版，请确认镜像包含 glibc 和动态加载器；musl-only 镜像应使用项目明确支持插件的 glibc 运行环境，而不是 `no-plugin` 包。

## 6. 启用后的验证

### 6.1 查看插件状态

使用管理认证访问 CPA 的插件列表接口：

```text
GET /v0/management/plugins
```

应能看到：

```text
id                 = enterprise-access-audit
enabled            = true
effective_enabled  = true
```

如果插件文件存在但 `effective_enabled` 不是 `true`，不要把它当作“没有策略”的正常状态，应先处理加载或配置错误。

### 6.2 打开插件管理页面

Management Center 不会把审计页面放入核心请求监控或 Enterprise Key 页面侧边栏。插件成功加载后，使用 Management Center 的插件资源入口打开 **Enterprise Access Audit** 工作区。该页面包含：

- 审计记录查询、筛选、分页和详情；
- 按 Key Hash 查看和编辑禁止模型；
- 审计默认开关、保留天数和最大文本长度设置。

Enterprise Key 页面中的模型访问策略控件也只在本插件 `effective_enabled=true` 时显示。插件未安装、被禁用或加载失败时，核心页面保持官方默认形式。

插件资源接口为：

```text
GET /v0/resource/plugins/enterprise-access-audit/ui
```

浏览器页面应通过已认证的 Management Center 打开，不要把管理认证信息写入插件页面或请求日志。

### 用户名展示和 Hash 对应关系

通过 Management Center 的插件资源页面打开时，插件会读取企业 Key 的**非敏感元数据投影**，将审计记录和访问策略优先显示为用户名。该投影只包含 API Key Hash、用户名、邮箱和部门 ID，不包含原始 API Key。

- 审计和策略的内部关联仍使用 CPA 的 8 位 Key Hash，这是插件执行请求策略所需的标识；
- 页面主标识显示企业 Key 的用户名，同一用户名拥有多个 Key 时会合并作为筛选条件；
- 页面同时显示 Hash 尾码，尾码采用完整 Hash 的最后 8 位，与请求监控页面保持一致；
- 如果插件页面无法访问元数据接口，则退回显示内部 Hash，不影响审计查询和策略保存；
- 用户名变化不会改变历史 JSONL 文件名，也不会把用户名写入文件名；文件仍按 Key Hash 和日期分区。

因此，日常查找应使用“用户名”筛选，而不是手工对照插件内部的 8 位 Hash。

插件隐藏资源页面中的“请求审计”支持直接输入用户名或邮箱关键字后筛选，不需要从下拉框中逐个查找用户。一个用户拥有多个 Enterprise Key 时，页面会将这些 Key 合并到同一次查询中；元数据暂时不可用时，应先恢复 `/v0/management/enterprise/key-bindings/metadata` 的管理访问权限。

## 7. Enterprise Key 禁止模型配置

模型目录保持完整可见，插件只在请求执行时校验策略。操作员可以在插件资源页面按用户名搜索用户，为单个用户设置禁止模型，也可以勾选多个用户后批量设置。

策略页面的模型选择器会读取当前 CPA 的管理模型目录和已加载认证文件的实际模型列表；读取不到目录时仍可手动输入动态模型 ID。批量保存模型限制会统一覆盖所选用户的禁止列表；如果只修改批量审计开关，则不会清空每个用户原有的模型限制。

规则：

- 模型 ID 会去除首尾空格、转换为 ASCII 小写、去重并排序；
- 第一阶段只支持精确匹配，不支持通配符、前缀或正则表达式；
- 同时检查客户端请求的模型和 CPA 解析/重写后的模型；
- 空禁止列表表示允许所有模型；
- 命中禁止规则时，在上游请求发送前返回 HTTP `403`，错误类型为 `model_not_allowed`；
- 策略按 API Key Hash 关联，不按用户名或邮箱作为唯一身份；
- 原始 API Key 不会写入策略、日志、文件名或管理接口响应。

## 8. 审计范围和内容

第一阶段审计标准文本请求，包括：

- OpenAI Chat Completions；
- OpenAI legacy Completions；
- OpenAI Responses；
- Claude Messages；
- Gemini 文本生成。

审计记录保存 Key Hash、时间、模型、来源格式、请求 ID、结果、状态码以及受限的用户文本。普通多轮对话只保存最后一个明确的 `role=user` 消息。迭代式 agent 请求经常会重复提交完整上下文；如果本次请求没有新增用户轮次，而只是 assistant、tool 或框架输出的继续迭代，则不会新增重复审计记录。

以下内容不会保存为用户正文：

- system、developer、assistant 历史消息；
- tool 参数、工具输出和 token ID；
- 图片、音频、视频等媒体内容；
- 原始请求 JSON；
- Authorization 头和原始 API Key。

Realtime/WebSocket、带 `execution_session_id` 的会话、token-count、模型列表、媒体请求及其他不在第一阶段路径矩阵内的请求不会保存用户文本，也不会应用本插件的 Enterprise Key 审计策略。

### cyber_policy 安全信号

只有上游响应中明确出现以下任一字段时，失败审计记录才会标记安全信号：

```json
{"error":{"code":"cyber_policy"}}
```

或：

```json
{"response":{"error":{"code":"cyber_policy"}}}
```

匹配必须是大小写敏感的精确值 `cyber_policy`。普通 HTTP 400/403/500、错误消息中的文字提及或本地模型拒绝，都不会被推断为该安全信号。安全消息会清理控制字符并限制长度，插件只提供人工审计，不会自动封禁用户。

## 9. 数据文件和备份

默认目录结构如下：

```text
.cli-proxy-api/plugins/enterprise-access-audit/
├── enterprise-access-audit.sqlite
├── key-abcdef12-20260830.jsonl
├── key-abcdef12-20260831.jsonl
└── key-12345678-20260831.jsonl
```

存储规则：

- 每个 API Key Hash 每个 UTC 日期一个 JSONL 文件；
- 文件名使用 `key-<hash>-<YYYYMMDD>.jsonl`，不包含用户名、邮箱或原始 API Key；
- SQLite 只保存策略、设置和迁移记录，不保存审计正文；
- 旧版本的 `key-<hash>.jsonl` 文件保持可读，新记录写入日期分区文件；
- 启动时会跳过损坏或截断的 JSONL 行，并执行必要的重复记录清理；
- 过期记录会按保留策略清理，清理后为空的日期文件会删除；
- 从旧 SQLite 审计正文存储升级时，旧数据库会先备份为 `.legacy-*.sqlite`，旧正文不会导入新的 JSONL 日志。

备份时同时保护 SQLite 和 JSONL 文件，并限制文件系统访问权限。不要把 API Key 放入备份文件名、工单、日志或压缩包密码提示中。Windows 请使用 NTFS ACL；macOS/Linux 请确保数据目录仅对 CPA 运行账户和受控运维账户可访问。

## 10. 常用管理接口

所有接口都需要 CPA Management API 的认证，不接受普通 provider API Key：

```text
GET /v0/management/enterprise-access-audit/policies
PUT /v0/management/enterprise-access-audit/policies/batch
PUT /v0/management/enterprise-access-audit/policy
GET /v0/management/enterprise-access-audit/audit
GET /v0/management/enterprise-access-audit/audit/detail?id=<id>
GET /v0/management/enterprise-access-audit/settings
PUT /v0/management/enterprise-access-audit/settings
```

接口中的身份字段只使用规范化的 API Key Hash。例如：

```json
{
  "key_hash": "abcdef12",
  "denied_models": ["gpt-4", "claude-3-5-sonnet"],
  "audit_enabled": true
}
```

不要在接口请求、浏览器开发者工具截图或问题报告中粘贴原始 API Key。

## 11. 故障排查

### 插件列表中没有 `enterprise-access-audit`

依次检查：

1. `plugins.enabled: true`；
2. `plugins.dir` 是否指向动态库所在的插件根目录；
3. `configs.enterprise-access-audit.enabled: true`；
4. 文件名是否为 `enterprise-access-audit.<ext>`；
5. `GOOS`、`GOARCH` 和动态库后缀是否匹配；
6. 是否误用了 Linux `*_no-plugin` 或 FreeBSD 包；
7. CPA 进程账户是否有权读取动态库及父目录；
8. 是否已重启 CPA 或完成配置重载。

### `effective_enabled` 为 `false`

这通常表示全局插件开关关闭、实例配置关闭、动态库加载失败或插件初始化/数据库配置失败。查看 CPA 启动日志和 `/v0/management/plugins` 返回的状态，不要通过清空策略或关闭审计来掩盖加载错误。

### Enterprise Key 页面没有模型策略控件

只有插件状态为 `effective_enabled: true` 时才显示模型策略控件。确认 CPA 已加载新插件，再刷新 Management Center；如果只替换了前端而没有重启 CPA，插件能力状态仍可能是旧状态。

### 审计页面没有新记录

确认：

- 本次请求属于第一阶段标准文本路径；
- Enterprise Key Hash 有效且策略中的审计开关已开启；
- 请求是新的用户轮次，而不是 agent 对已有上下文的继续迭代；
- 没有超过 `retention_days`；
- 查看的是正确的 Key Hash 和 UTC 日期文件；
- 插件已被当前 CPA 进程加载，而不是只替换了磁盘上的动态库。

### 动态库加载失败

优先重新下载同版本、同平台、同架构的 CPA 发布包，并校验 Release 提供的 `checksums.txt`。不要把其他操作系统、其他架构或 `no-plugin` 包中的文件强行改名后加载。动态库是进程内代码，无法像普通外部进程一样隔离运行。

## 12. 从源码构建

源码位于：

```text
plugins/enterprise-access-audit/go
```

普通测试和包构建：

```bash
cd plugins/enterprise-access-audit/go
go test ./...
go test -race ./...
go build ./...
```

C-shared 动态库需要目标平台的 CGO C 编译器，并且必须为实际运行的 `GOOS/GOARCH` 构建。Windows 下可以使用仓库提供的 PowerShell 脚本：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\plugins\enterprise-access-audit\build.ps1
```

不要把普通 Go 的 `-buildmode=plugin` 产物当作 CPA 插件。CPA 使用现有的 C-shared JSON ABI：

- Windows：`-buildmode=c-shared` 生成 `.dll`；
- macOS：生成 `.dylib`；
- Linux：生成 `.so`。

生产部署优先使用与 CPA 同版本的官方发布包，避免自行交叉编译出 ABI、C 运行库或架构不匹配的动态库。
