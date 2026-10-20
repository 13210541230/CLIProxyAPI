CLIProxyAPI Suite 用户指南
==========================

如何阅读
--------
1. 解压整个 ZIP 文件，保持目录结构不变。
2. 使用 Microsoft Edge、Google Chrome 或其他现代浏览器打开 index.html。
3. 从首页进入下载安装、OAuth、CPA-Manager 管理面板、Enterprise Access Audit 专题和配置与风控基线。

离线使用
--------
本指南的页面、导航和样式均包含在本目录中；解压后可离线浏览，不需要启动服务或运行脚本。GitHub、CLIProxyAPI 官方文档等外部参考链接需要联网。

文件结构
--------
index.html                  文档首页
install.html                Release 下载、解压、配置及启停
manager.html                以 CPA-Manager（默认端口 18317）为主线的管理操作
oauth.html                  从 CPA-Manager 登录 OAuth 并验证代理请求
enterprise-audit.html       Enterprise Access Audit 插件操作
hardening.html              客户端配置与风控基线（codex config.toml、CPA 侧配置、账号运营规范）
../hardening-quick.html     风控预防速查（独立页，位于 docs/ 根目录）
assets/site.css             页面样式（请与 HTML 文件一同保留）
README.txt                  本文件

下载来源
--------
本指南对应的 CLIProxyAPI-Suite 联合包请从以下仓库的 GitHub Releases 获取：
https://github.com/13210541230/CLIProxyAPI/releases

注意：本 ZIP 仅包含用户指南，不包含 CLIProxyAPI、CPA-Manager、插件或 OAuth 凭据。
