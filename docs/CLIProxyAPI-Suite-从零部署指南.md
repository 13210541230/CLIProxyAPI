# CLIProxyAPI Suite 从零部署指南

本文以 **CLIProxyAPI + CPA-Manager 联合发布包**为例，介绍下载、解压、基础安全配置、OAuth 登录、代理验证、管理面板和 `enterprise-access-audit` 插件的基本用法。适用于 Windows、Linux 和 macOS。版本与界面可能随 Release 更新；生产部署请优先阅读下载包内的 `SUITE-README_CN.md` 和对应版本的插件说明。另提供[可浏览的 HTML 文档集](cliproxyapi-suite/index.html)；通用安装概念参考[官方快速开始](https://help.router-for.me/cn/introduction/quick-start.html)，本项目 Suite 下载仍以你们的 [GitHub Releases](https://github.com/13210541230/CLIProxyAPI/releases) 为准。

## 1. 先认识这套程序

- **CLIProxyAPI（CPA）**：代理服务，默认监听 `8317`，负责接收兼容 API 请求、选择 OAuth 账号或 API 提供商并转发到上游。
- **CPA-Manager**：管理主入口，默认地址为 `http://127.0.0.1:18317/management.html`；完成初始化后可在同一面板完成日常管理，它与 CPA 是两个进程。
- **enterprise-access-audit**：随支持插件的 CPA 发布包提供的动态库插件，扩展 Enterprise Key 的模型策略、请求审计和可选 Codex 账号池能力。
- **cpa-updater**：联合包的更新程序。Manager“系统”页面可检查版本并下载更新包；实际校验、安装和重启由 `cpa-updater` 执行。

本指南以 CPA-Manager（18317）作为管理操作主入口：初始化连接后，可在一个面板完成配置、OAuth 授权、认证文件、监控、配额和插件操作。CPA 的 8317 是后端服务地址；`http://127.0.0.1:8317/management.html` 仅作为需要直接访问 CPA 原生管理界面时的备用入口，日常操作优先在 CPA-Manager 完成。

## 2. 下载正确的 GitHub Release

联合包的唯一发布入口：

**https://github.com/13210541230/CLIProxyAPI/releases**

在最新稳定 Release 的 Assets 中选择名称以 `CLIProxyAPI-Suite_` 开头的包，不要误选只有 CPA 的独立归档。命名格式如下：

```text
CLIProxyAPI-Suite_<版本>_<平台>_<架构>.zip       # Windows
CLIProxyAPI-Suite_<版本>_<平台>_<架构>.tar.gz    # Linux / macOS
```

平台和架构示例：

| 系统 | 常见包名中的平台/架构 | 适用设备 |
|---|---|---|
| Windows x64 | `windows_amd64` | 大多数 Intel / AMD 电脑和服务器 |
| Windows ARM64 | `windows_aarch64` | ARM Windows 设备 |
| Linux x64 | `linux_amd64` | 大多数 x64 Linux 服务器 |
| Linux ARM64 | `linux_aarch64` | ARM Linux 服务器 |
| macOS Intel | `darwin_amd64` | Intel Mac |
| macOS Apple Silicon | `darwin_aarch64` | M 系列 Mac |

下载包的同时建议下载同一 Release 中的 `suite-checksums.txt`，核对归档的 SHA-256。Windows PowerShell 示例：

```powershell
Get-FileHash .\CLIProxyAPI-Suite_<版本>_windows_amd64.zip -Algorithm SHA256
```

Linux/macOS 示例：

```bash
sha256sum CLIProxyAPI-Suite_<版本>_linux_amd64.tar.gz
# macOS 也可用：shasum -a 256 <下载的归档文件>
```

将计算结果与校验文件中相同归档名对应的 SHA-256 比较。不要把管理密钥、API Key 或 OAuth 凭据发给他人协助校验。

## 3. 解压并准备配置

1. 将归档解压到长期使用、当前运行用户可读写的目录，例如 `D:\CLIProxyAPI-Suite`、`/opt/cliproxyapi` 或用户目录下的专用文件夹。
2. 目录中应能看到 `cli-proxy-api`、`cpa-manager`、`cpa-updater`、`config.example.yaml`、启动/停止脚本，以及 `plugins`（插件兼容包中）。Windows 文件名带 `.exe`。
3. 首次启动脚本会在缺少 `config.yaml` 时从模板复制一份并继续启动。建议先手动复制模板并编辑，再启动服务，避免带着示例密钥对外提供服务：

```text
config.example.yaml → config.yaml
```

4. 打开 `config.yaml`，至少替换管理密钥和客户端 API Key。仅本机使用时可采用以下基础配置片段（保留模板中的其他默认配置）：

```yaml
host: "127.0.0.1"
port: 8317

auth-dir: "~/.cli-proxy-api"

api-keys:
  - "替换为足够长且随机的客户端API密钥"

remote-management:
  allow-remote: false
  secret-key: "替换为另一条独立的管理密钥"
  disable-control-panel: false
```

配置说明：

- `api-keys` 是调用 `/v1/...` 等代理 API 时使用的客户端密钥；`remote-management.secret-key` 是管理接口/面板认证密钥。两者用途不同，应设置为不同的随机值。
- `host: "127.0.0.1"` 将 CPA 限制为本机访问，适合先完成安装与验证。模板默认 `host: ""`，表示监听所有网卡；不要在不了解防火墙和访问控制的情况下直接暴露到公网。
- `remote-management.allow-remote: false` 限制远程管理访问。它控制的是管理接口，不等同于代理 API 的监听地址设置。
- `auth-dir` 存放 OAuth 凭据。默认在当前用户主目录下的 `.cli-proxy-api`；请确保该目录有备份和访问权限保护，不要放进公开 Web 目录。
- 模板中的示例 API Key 不能原样用于生产。管理密钥留空会禁用管理 API；配置明文管理密钥后 CPA 启动时会进行哈希处理。

如果确实需要让局域网内其他设备访问代理，应将 `host` 调整为合适的内网地址或按网络规划监听，并使用防火墙限制来源。远程打开管理页面还需要单独评估 `allow-remote`、TLS/反向代理和管理认证，不要仅为了远程调用 API 就开放管理接口。

## 4. 启动、停止与打开管理页面

从解压目录启动：

| 系统 | 启动 | 停止 |
|---|---|---|
| Windows | 双击 `start.bat` | 双击 `stop.bat` |
| Linux / macOS | `./start.sh` | `./stop.sh` |

脚本先启动 CPA，再启动 CPA-Manager，并记录由该套件启动的进程。请使用配套停止脚本，不要用不相关进程名批量结束服务。

如需使用其他配置文件：

```powershell
.\start.bat --config D:\cpa-config\config.yaml
```

```bash
./start.sh --config /etc/cliproxyapi/config.yaml
```

在本机浏览器打开：

- **CPA-Manager 日常管理主入口**：`http://127.0.0.1:18317/management.html`
- **CPA 原生管理页（备用）**：`http://127.0.0.1:8317/management.html`。只有在 Manager 不可用或需要直接诊断 CPA 时再使用。

首次进入 CPA-Manager 时按初始化向导填写 CPA 连接地址（默认 `http://127.0.0.1:8317`）和 `remote-management.secret-key`，再点击登录；客户端调用 API 的 `api-keys` 不是管理密码。请求监控和采集间隔可先保留默认值。若自定义了 CPA 端口，连接地址也要同步修改。

常用管理区域大体包括：

- **CPA-Manager 配置页**：管理 CPA 服务配置、客户端 API Key、连接和常用参数。
- **CPA-Manager OAuth / 认证文件页**：从 OAuth 页面添加账号，在认证文件页查看、筛选或按需管理凭据。
- **CPA-Manager 监控 / 配额页**：查看请求和用量情况，以及已授权账号的配额信息。
- **CPA-Manager 系统 / 插件资源页**：查看系统状态、检查更新，并打开 Enterprise Access Audit 等插件页面。菜单名称随版本及已加载插件变化。
- **系统更新**：Manager 的系统页负责检查版本和下载包；需要自动安装时，在通过启动脚本运行的套件目录执行 `./cpa-updater`（Windows PowerShell 使用 `.\cpa-updater.exe`）。默认执行会校验并安装更新、重启服务；`--check` 仅检查版本。更新前仍建议备份配置、OAuth 文件及插件数据。

日志位于套件目录的 `logs` 文件夹。Windows 启动脚本会分别写 CPA 和 Manager 的标准输出/错误日志；Linux/macOS 脚本写入 CPA 与 Manager 日志文件。遇到启动失败时先检查这些日志和端口占用。

## 5. 添加 OAuth 登录账号

OAuth 登录统一从 CPA-Manager（18317）操作；登录成功后，凭据由 CPA 保存到 `auth-dir` 并用于上游请求。

1. 确认 Suite 已运行，打开 `http://127.0.0.1:18317/management.html`。
2. 首次进入时完成初始化向导：CPA 连接地址填 `http://127.0.0.1:8317`，管理密码填 `config.yaml` 的 `remote-management.secret-key`。按需启用请求监控，采集间隔先保留默认值，然后点击登录。
3. 从 CPA-Manager 导航进入 **OAuth** 页面，选择当前版本列出的服务商登录方式（例如 Codex、Anthropic/Claude、Gemini CLI、Antigravity）。
4. 点击开始登录，在服务商页面使用自己有权使用的账号确认授权。
5. 授权成功后返回 CPA-Manager 的 OAuth 页面查看状态；必要时在认证文件或配额页面确认账号已出现。多个账号逐个完成授权。
6. 如果浏览器无法回到本机回调地址，不要重复授权：复制浏览器地址栏的**完整回调 URL**，回到 CPA-Manager 的 OAuth 页面粘贴并提交。回调 URL 可能包含一次性授权信息，不要转发。
7. 使用代理地址 `http://127.0.0.1:8317/v1`、客户端 `api-keys` 和实际模型 ID 做 API 验证。

OAuth 凭据具有敏感性。不要把 `auth-dir`、令牌文件或回调 URL 加入代码仓库、公开网盘、截图或工单。账号可用配额与服务商策略由服务商决定。

## 6. 验证代理请求

先使用配置中的客户端 API Key 查询可用模型。Windows PowerShell 请调用 `curl.exe`，避免旧版 PowerShell 中 `curl` 别名造成参数差异：

```powershell
curl.exe http://127.0.0.1:8317/v1/models `
  -H "Authorization: Bearer 替换为你的客户端API密钥"
```

Linux/macOS：

```bash
curl http://127.0.0.1:8317/v1/models \
  -H 'Authorization: Bearer 替换为你的客户端API密钥'
```

从返回内容中选取实际模型 ID 后，再向对应协议端点发送请求，例如 OpenAI Chat Completions：

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H 'Authorization: Bearer 替换为你的客户端API密钥' \
  -H 'Content-Type: application/json' \
  -d '{"model":"替换为返回的模型ID","messages":[{"role":"user","content":"回复：连接成功"}]}'
```

收到模型响应即表示客户端到 CPA、上游账号的基本链路已打通。若模型不支持 Chat Completions，请使用该模型对应的协议端点。

客户端常用连接信息：

- Base URL：`http://127.0.0.1:8317/v1`
- API Key：`config.yaml` 的 `api-keys` 中配置的值
- Model：`/v1/models` 返回的实际模型 ID

客户端与 CPA 不在同一台机器时，应把 `127.0.0.1` 换成 CPA 可达的内网地址，并同时检查监听地址、防火墙及传输加密设置。

## 7. `enterprise-access-audit` 插件

### 7.1 安装与启用

支持插件的联合包已包含与平台/架构匹配的动态库，通常位于：

```text
plugins/<系统>/<架构>/enterprise-access-audit.<扩展名>
```

例如 Windows x64 为 `plugins/windows/amd64/enterprise-access-audit.dll`，Linux x64 为 `.so`，macOS 为 `.dylib`。`*_no-plugin` 和不兼容平台包不能加载该插件。使用同一 Release 的完整 Suite 包，不要把其他版本或其他架构的 DLL/so/dylib 混放进来。

在 `config.yaml` 的 `plugins` 配置中启用全局插件和本插件。与模板中的其他配置合并，不要重复创建第二个 `plugins:` 根键：

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
      audit_enabled: false
      default_audit_enabled: false
      max_text_bytes: 32768
```

`data_dir`、数据库等相对路径以 CPA 的**工作目录**为基准。保存配置后用 `stop` 停止套件，再重新运行 `start`，使动态库在 CPA 启动时加载。

检查 CPA-Manager 的插件页面/资源列表中是否出现 Enterprise Access Audit。也可使用管理认证检查 CPA 插件状态接口 `GET /v0/management/plugins`；`enterprise-access-audit` 应显示已加载且有效启用。插件未加载时不要继续假设策略已生效，先检查 CPA 日志、全局开关、插件文件名和系统/架构路径。

### 7.2 在管理页面中的主要用法

插件成功加载后，通过 CPA-Manager 中的插件资源入口打开 **Enterprise Access Audit**：

- **模型访问策略**：按 Enterprise Key 配置禁止模型列表。策略按 API Key Hash 关联；命中时在请求发往上游前拒绝。系统仍可展示完整模型目录。
- **请求审计**：可查看、筛选审计记录，并按 Key 设置是否审计。需要同时打开插件全局 `audit_enabled` 和目标 Key 的审计开关；`default_audit_enabled` 只决定尚无单独设置的 Key 默认值。新安装默认关闭审计。
- **保留与文本长度**：在插件设置中查看/调整记录保留时间及单条用户文本上限。默认数据目录保存 SQLite 策略/设置数据与按日期分区的 JSONL 审计记录；请按敏感业务数据保护它们并纳入受控备份。
- **Cyber-policy 人工复核**：只有上游错误结构明确返回 `error.code` 或 `response.error.code` 等于 `cyber_policy` 时才标记该安全信号。普通 HTTP 4xx/5xx、文本中提及该词或本地拒绝都不会推断为该信号。插件只供人工审计，不会自动封禁用户。
- **数据最小化**：审计保存必要请求元数据和受限的用户文本；不保存 system/developer 提示、assistant 历史、工具参数或原始图片/音频/视频内容。原始 API Key 不作为审计身份保存，使用其 Hash 关联记录。

账号池是同一插件中的可选 Codex 调度功能，不是启用审计的前置条件。首次部署建议保持关闭；确需使用时，在插件的账号池页面按实际账号、用户绑定及并发策略完成配置，并先阅读[插件完整说明](../plugins/enterprise-access-audit/README_CN.md)。启用账号池调度前，应关闭其他会竞争 Codex 选号/并发控制的插件；不要只复制 DLL 就假定已有池策略。

## 8. 常见问题

| 现象 | 检查方向 |
|---|---|
| CPA-Manager（18317）无法打开 | 检查 CPA-Manager 进程及端口占用，并查看 Manager 日志。 |
| CPA-Manager 无法连接 CPA | 确认 CPA 与 Manager 都运行；Manager 中 CPA 地址/端口正确；`secret-key` 与 `config.yaml` 一致；远程连接时检查 `allow-remote` 和防火墙。 |
| CPA-Manager 无法登录 | 确认 `remote-management.secret-key` 非空，输入的是管理密钥而非客户端 `api-keys`；检查 CPA 管理接口配置。 |
| OAuth 授权后没有账号 | 按 OAuth 页面要求提交完整回调 URL；检查浏览器到服务商的网络连通性、CPA 日志和 `auth-dir` 的读写权限。 |
| `/v1/models` 返回 401 | 请求头使用了 `api-keys` 中的客户端 Key，而不是 `secret-key`；检查是否有多余空格或使用了旧 Key。 |
| 插件页不可见/插件无效 | 检查 `plugins.enabled`、插件条目 `enabled`、动态库平台与架构、文件名及 CPA 启动日志；重启 CPA 后再次检查状态。 |
| 模型拒绝策略有配置但请求仍通过 | 确认插件状态为有效启用、策略绑定的是本次请求使用的 Key，并确认请求模型 ID 与策略相符。 |
| 审计没有新记录 | 审计全局开关和目标 Key 开关都必须开启；确认请求属于插件支持的标准文本请求范围，并检查当前 Key 与保留设置。 |

## 9. 继续阅读

- [联合发布包说明](../release/unified-release.md)
- [enterprise-access-audit 插件完整说明](../plugins/enterprise-access-audit/README_CN.md)
- [CLIProxyAPI 中文项目说明](../README_CN.md)
- [官方快速开始](https://help.router-for.me/cn/introduction/quick-start.html)
