# Sealtun

[English Version](./README_EN.md)

Sealtun 是一款功能强大、设计优雅的 CLI 工具，旨在为 **Sealos Cloud** 和 **Kubernetes** 用户提供类似 `cloudflared` 的内网穿透体验。

它通过动态调度 Kubernetes 资源（Deployments, Services, Ingresses），并利用双向多路复用 WebSocket 流（`yamux`）建立安全隧道，将你的本地开发环境直接暴露到公网。

## ✨ 特性

- 🔑 **无密码 OAuth2 登录**：使用设备授权流（Device Authorization Grant）通过 `sealtun login` 轻松连接。
- 🌍 **区域切换**：支持查看已内置的 Sealos Cloud region，并通过 `sealtun region use` 重新登录切换区域。
- 👤 **Profile 多账号管理**：可把不同 Sealos 账号、region、workspace 和 kubeconfig 保存为命名 profile，按需切换。
- 🚀 **一键暴露服务**：执行 `sealtun expose 8080`，即可获得一个受信任的 HTTPS URL，将流量安全地路由到本地。
- 🌐 **自定义域名自动化**：可用 `domain plan/add/verify/status/doctor` 生成 CNAME 指引、等待 DNS、绑定域名并检查证书状态。
- 📊 **状态、诊断与工作台**：`doctor <tunnel-id>`、`inspect --remote`、`logs`、`events`、`metrics` 和 `dashboard` 可定位本地端口、daemon、远端 Pod、Service、Ingress 与证书问题，也可在本地工作台中管理隧道。
- 🧩 **协议模板**：`template https|ssh|tcp|mysql|postgres|redis|mqtt` 可生成直接命令和 `sealtun.yaml` 示例。
- 🧾 **声明式配置**：`apply -f sealtun.yaml` 可用 YAML 声明隧道，并以稳定名称幂等创建或更新。
- 🌐 **深度适配 Sealos**：原生使用 Sealos Cloud 的 Kubernetes、Service 与 Ingress 能力，当前稳定支持 HTTPS 入口和 WebSocket 隧道。
- 🐳 **全能二进制文件**：客户端和服务器代理共用同一个精简的二进制文件和 Docker 镜像。
- ☸️ **云原生设计**：完全使用标准的 Kubernetes API 管理资源，无需额外的复杂中间件。

## 📦 安装

推荐通过 npm 安装 `sealtun` CLI，也可以直接从 GitHub Releases 下载对应平台的二进制；远端隧道 Pod 使用同版本的 `ghcr.io/gitlayzer/sealtun` 镜像。

使用 npm 全局安装：

```bash
npm install -g sealtun
sealtun --version
```

使用 npx 临时运行：

```bash
npx sealtun@latest --version
npx sealtun@latest login
```

npm 包会按当前系统自动安装对应平台的可选二进制依赖，当前支持 macOS、Linux、Windows 的 `amd64/x64` 与 `arm64`。

macOS / Linux 快速安装：

```bash
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

curl -L "https://github.com/gitlayzer/sealtun/releases/latest/download/sealtun_${OS}_${ARCH}.tar.gz" -o sealtun.tar.gz
tar -xzf sealtun.tar.gz sealtun
chmod +x sealtun
sudo mv sealtun /usr/local/bin/sealtun
sealtun --version
```

Windows PowerShell 快速下载：

```powershell
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
Invoke-WebRequest -Uri "https://github.com/gitlayzer/sealtun/releases/latest/download/sealtun_windows_$arch.zip" -OutFile sealtun.zip
Expand-Archive .\sealtun.zip -DestinationPath .
.\sealtun.exe --version
```

从源码构建本地调试版本：

```bash
git clone https://github.com/gitlayzer/sealtun.git
cd sealtun
make build
./sealtun --version
```

`make build` 默认会把当前 Git short hash 注入到本地二进制的 version 中，用于确认本地二进制和已 push 的代码提交一致。未打 tag 的本地构建会使用 `latest` 远端隧道镜像；正式 tag 发布时，GitHub Actions 会用 tag 版本构建 GitHub Release 产物和同版本容器镜像。

## 🚢 发版流程

项目采用 tag 驱动发布：

```bash
# 1. 先完成测试、提交并 push 分支
go test ./...
make build
git push origin master

# 2. 再创建并 push 语义化版本 tag
git tag vX.Y.Z
git push origin vX.Y.Z
```

推送 `v*` tag 后，GitHub Actions 会触发 GoReleaser 生成多平台二进制和 GitHub Release；Docker workflow 会同步构建并发布 `ghcr.io/gitlayzer/sealtun` 镜像。发版后建议重新执行 `make build && ./sealtun --version`，确认本地二进制显示的 Git hash 与已 push 的提交一致。

GitHub Release 产物构建完成后，再发布 npm 包：

```bash
NPM_VERSION=X.Y.Z NPM_RELEASE_TAG=vX.Y.Z make npm-publish-dry-run
NPM_VERSION=X.Y.Z NPM_RELEASE_TAG=vX.Y.Z make npm-publish
```

`make npm-publish` 会先从对应 GitHub Release 下载 GoReleaser 生成的二进制资产，生成本地 `packages/` 目录，然后先发布各平台可选依赖包，最后发布主包。`packages/` 是发包中间产物，已被 `.gitignore` 忽略，不提交到远端。

## 🤖 Codex Skill

仓库内置了 `skills/sealtun`，用于让 Codex 类 AI agent 更准确地理解和使用 Sealtun CLI。这个 skill 会在用户提到 `sealtun`、`sealtun.yaml`、内网穿透、本地端口暴露、临时公网预览链接、第三方回调到本地、隧道访问控制、公网 SSH 或 TCP 隧道等场景时被动匹配。

skill 触发后会先判断是否真的属于“本地/dev 服务通过 Sealtun 暴露到公网”的范围，再按用法指导、实际操作或排障流程执行；没有明确要求时，不会擅自运行 `sealtun expose/apply/domain set/stop/cleanup/logout` 这类会改变本地或云端状态的命令。

推荐直接从仓库安装 skill：

```bash
npx skills add https://github.com/gitlayzer/sealtun
```

如果是在本仓库本地开发，也可以把该目录同步到 Codex 的全局技能目录：

```bash
mkdir -p ~/.codex/skills
cp -R skills/sealtun ~/.codex/skills/sealtun
```

## 🚀 快速上手

### 1. 登录到 Sealos
执行设备认证（类似于 `gh auth login`，无需手动输入密码）：
```bash
sealtun login

# 查看支持的 region
sealtun region list

# 切换到指定 region
sealtun region use hzh

# 登录并保存为命名 profile
sealtun login gzg --profile gzg-main

# 查看和切换已保存 profile
sealtun profile list
sealtun profile use hzh-dev
```
内置 region：

| 名称 | Region API | Ingress 域名后缀 |
| --- | --- | --- |
| `gzg` | `https://gzg.sealos.run` | `sealosgzg.site` |
| `hzh` | `https://hzh.sealos.run` | `sealoshzh.site` |
| `bja` | `https://bja.sealos.run` | `sealosbja.site` |
| `cloud` | `https://cloud.sealos.io` | `cloud.sealos.io` |
| `usw` | `https://usw-1.sealos.io` | `usw-1.sealos.app` |

*注：目前仅支持内置的 Sealos Cloud region。登录会获取 Kubernetes 凭据和当前 region 的 `SEALOS_DOMAIN`，并安全地存储在 `~/.sealtun` 目录中。命名 profile 会保存到 `~/.sealtun/profiles/<name>`，切换 profile 时会同步切换 active `auth.json` 与 `kubeconfig`。*

### 2. 暴露本地端口
例如，让运行在本地 `3000` 端口的 Web 服务可以被公网访问：
```bash
# 默认使用 https 协议 (兼容普通 HTTP 与 WebSocket 应用流量)
sealtun expose 3000

```

为公网业务流量启用 Basic Auth：
```bash
# 推荐：从环境变量读取密码，避免进入 shell history
export SEALTUN_BASIC_AUTH_PASSWORD='change-me'
sealtun expose 3000 --basic-auth-user admin --basic-auth-password-env SEALTUN_BASIC_AUTH_PASSWORD

# 也支持一次性写法
sealtun expose 3000 --basic-auth admin:change-me
```

Basic Auth 由 Sealtun server 代理层校验，不依赖 Ingress annotation；它只保护公网业务路径，不会拦截 `/_sealtun/ws` 隧道控制通道、健康检查或受内部 Bearer secret 保护的 metrics。

也可以启用不依赖 Ingress 的访问策略：
```bash
# Bearer Token
export SEALTUN_BEARER_TOKEN='share-secret'
sealtun expose 3000 --bearer-token-env SEALTUN_BEARER_TOKEN

# IP allowlist / denylist，支持单个 IP 或 CIDR
sealtun expose 3000 --ip-allowlist 203.0.113.10,198.51.100.0/24 --ip-denylist 198.51.100.9

# 临时访问链接，默认 1 小时后失效
export SEALTUN_TEMP_TOKEN='review-link-secret'
sealtun expose 3000 --temporary-access-token-env SEALTUN_TEMP_TOKEN --temporary-access-ttl 1h
```

Bearer Token 和临时链接 token 至少需要 8 个字符，只保存 SHA-256 hash，不会写入 Deployment 参数；临时链接使用 `?_sealtun_token=...` 访问，Sealtun 会在转发到本地服务前移除该查询参数。IP 规则优先使用 Ingress/代理传入的 `X-Real-IP`，再回退到 `X-Forwarded-For` 中最后一个有效的代理确认客户端 IP。Basic Auth 与 Bearer/临时链接同时配置时，任一认证方式通过即可访问。

Sealtun 会自动执行以下操作：
1. 在你的 Sealos Namespace 中启动一个隧道代理 Pod。
2. 配置 Ingress 路由规则。
3. 建立加密 WebSocket 隧道，并将所有流量转发至 `localhost:3000`。

### 3. SSH 公网访问
如果 Sealos Region 支持公网 TCP NodePort，可以用四层 SSH 模式直接连接公网域名和端口：

```bash
# macOS/Linux 常见 SSH 端口是 22；也可以换成本机 sshd 监听的其他端口
sealtun expose 22 --protocol ssh
```

命令会输出公网 SSH 入口：
```bash
ssh <user>@<public-host> -p <node-port>
```

也可以写进 `~/.ssh/config`，之后直接 `ssh sealtun-dev`：
```sshconfig
Host sealtun-dev
  HostName <public-host>
  User <user>
  Port <node-port>
```

`--protocol ssh` 的公网业务入口只有 TCP NodePort，不会提供默认 HTTPS 业务 URL。Sealtun 仍会保留内部控制通道供本地 daemon 连接远端 Pod，但它不作为 SSH 隧道的用户访问入口。Basic Auth、Bearer Token、临时链接、IP 规则和自定义域名只适用于 HTTPS 隧道，不适用于 SSH 四层入口。旧的 WebSocket ProxyCommand 备用方式仍可用：

```bash
ssh -o ProxyCommand='sealtun ssh connect <tunnel-id>' <user>@sealtun
```

### 4. 通用 TCP 公网访问
除 SSH 外，也可以用通用四层 TCP 暴露本地数据库、调试服务或其他非 HTTP 协议：

```bash
sealtun expose 5432 --protocol tcp
```

命令会输出公网 TCP 入口：
```bash
<public-host>:<node-port>
```

`--protocol tcp` 和 `--protocol ssh` 一样走公网 TCP NodePort，只保留 HTTPS 控制通道供本地 daemon 连接远端 Pod，不提供默认 HTTPS 业务 URL。Basic Auth、Bearer Token、临时链接、IP 规则和自定义域名属于 HTTPS 代理层能力，不适用于 TCP 四层入口。

### 5. 使用自定义域名
新建隧道时先生成官方 Sealos 域名和 CNAME 目标：
```bash
sealtun expose 3000 --domain app.example.com

# 如果你会在命令等待期间配置 DNS，可以等待 CNAME 验证、绑定和证书就绪
sealtun expose 3000 --domain app.example.com --wait-domain
```

或者在 DNS 生效后对已有隧道绑定：
```bash
# 先查看需要配置的 DNS
sealtun domain plan <tunnel-id> app.example.com

# DNS 已经生效后绑定
sealtun domain set <tunnel-id> app.example.com

# 或者等待 DNS 生效后自动绑定，并继续等待证书就绪
sealtun domain add <tunnel-id> app.example.com --wait --timeout 5m
```

Sealtun 会保留一个 Sealos 官方子域名作为隧道控制面和 CNAME 目标。只有 CNAME 已经指向该 Sealos host 后，Sealtun 才会把自定义域名写入 Ingress，并创建 cert-manager `Issuer` 与 `Certificate`。你需要在自己的 DNS 服务商处配置：
```text
CNAME app.example.com -> <sealos-host>
```

验证 CNAME、Ingress 与证书状态：
```bash
sealtun domain verify <tunnel-id>

# 持续等待，直到 DNS 与证书就绪或超时
sealtun domain verify <tunnel-id> --wait --timeout 5m

# 汇总所有自定义域名状态
sealtun domain status

# 对单个域名做更详细诊断
sealtun domain doctor <tunnel-id>
```

移除自定义域名：
```bash
sealtun domain clear <tunnel-id>
```

### 6. 观测和本地控制台
查看远端隧道 Pod 日志：
```bash
sealtun logs <tunnel-id>
sealtun logs <tunnel-id> --tail 200
sealtun logs <tunnel-id> --follow
```

查看隧道指标：
```bash
sealtun metrics <tunnel-id>
sealtun metrics <tunnel-id> --json
```

查看远端 Kubernetes 事件：
```bash
sealtun events <tunnel-id>
sealtun events <tunnel-id> --json
```

一键诊断本地与远端状态：
```bash
# 全局健康检查
sealtun doctor

# 单条隧道诊断，会给出本地端口、daemon、远端资源和下一步建议
sealtun doctor <tunnel-id>
sealtun doctor <tunnel-id> --json
```

`metrics` 会聚合本地 session 状态、远端 Deployment/Pod/Ingress 状态，并在远端 Pod 支持时读取受 Bearer secret 保护的 `/_sealtun/metrics` 请求计数。TCP/SSH 四层隧道还会暴露 TCP 连接数、活跃连接数、字节数和错误数。

启动本地工作台：
```bash
sealtun dashboard

# 自定义监听地址
sealtun dashboard --addr 127.0.0.1 --port 19777
```

Dashboard 默认仅监听本地地址，数据来自当前 active profile/region/namespace 的本地 session、登录状态、远端诊断和自定义域名状态。页面可以创建 HTTPS/SSH/TCP 隧道、执行 `sealtun.yaml` 的 dry-run/diff/apply、stop/start/cleanup 隧道、查看 logs/metrics/events，并执行 domain plan/add/verify/clear。

```bash
# 允许远程访问工作台，仅建议在可信网络临时使用
sealtun dashboard --addr 0.0.0.0 --allow-remote
```

远程模式不会把 dashboard token 写进 HTML；访问者需要 URL fragment 或请求头中的 token。所有写操作都要求页面确认，并由后端再次校验 `confirm` 字段，避免误触或脚本误调用。

### 7. 协议模板
不确定该怎么写命令或声明式配置时，可以先生成模板：

```bash
sealtun template https --name web --port 3000 --domain app.example.com
sealtun template ssh
sealtun template postgres
sealtun template redis --name cache
```

模板会同时输出一次性 `sealtun expose` 命令和可提交到项目内的 `sealtun.yaml` 片段。`mysql`、`postgres`、`redis`、`mqtt` 模板默认走通用 TCP 四层入口；HTTPS 模板才支持自定义域名和访问控制。

### 8. 声明式配置
创建 `sealtun.yaml`：
```yaml
version: v1
tunnels:
  - name: web
    localPort: 3000
    protocol: https
    domain: app.example.com
    ttl: 2h
    basicAuth:
      credential: admin:change-me
    accessPolicy:
      bearerTokenEnv: SEALTUN_BEARER_TOKEN
      ipAllowlist:
        - 203.0.113.10
        - 198.51.100.0/24
      ipDenylist:
        - 198.51.100.9
      temporaryLinks:
        - name: review
          tokenEnv: SEALTUN_TEMP_TOKEN
          ttl: 1h
    waitDomain: false
    readyTimeout: 90s
    domainTimeout: 5m
```

应用配置：
```bash
# 离线校验和预览，不需要登录
sealtun apply -f sealtun.yaml --dry-run

# 对比本地 session 与声明式配置
sealtun diff -f sealtun.yaml

# 创建或更新隧道
sealtun apply -f sealtun.yaml
```

也可以使用展开的明文写法：
```yaml
basicAuth:
  username: admin
  password: change-me
```

或从环境变量读取密码：
```yaml
basicAuth:
  username: admin
  passwordEnv: SEALTUN_BASIC_AUTH_PASSWORD
```

`name` 会作为稳定 tunnel ID 使用，因此重复执行 `apply` 会更新同一个 `sealtun-<name>` 资源。`tunnels` 支持一次声明多条隧道；`ttl` 会写入本地 session 的 `expiresAt`，本地 daemon 发现过期后会自动删除远端资源和本地记录。自定义域名仍然遵循 CNAME 先验证再绑定的规则；新隧道如果 CNAME 未就绪，`apply` 会先保留 Sealos 官方域名并输出后续 `domain set` 指令；已有隧道则会拒绝未验证的自定义域名变更，避免误清理或覆盖正在使用的域名配置。

## 🛠️ 架构详情

- **HTTPS 隧道协议**：基于 WebSocket 的 Yamux 多路复用。
- **SSH 四层入口**：`--protocol ssh` 只提供公网 TCP NodePort 直连本地 SSH；HTTPS 只作为内部控制通道，不提供默认业务 URL。
- **通用 TCP 四层入口**：`--protocol tcp` 提供公网 TCP NodePort，可暴露本地数据库、消息队列、调试服务等非 HTTP 协议。
- **Sealos 资源**：触发 `sealtun expose` 时，会在集群中创建以 `sealtun-*` 命名的 `Deployment`、`Service` 和 `Ingress`。
- **镜像来源**：依赖于 `ghcr.io/gitlayzer/sealtun` 的原生镜像。

## 📄 许可证

MIT License.
