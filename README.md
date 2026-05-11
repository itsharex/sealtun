# Sealtun

[English Version](./README_EN.md)

Sealtun 是一款功能强大、设计优雅的 CLI 工具，旨在为 **Sealos Cloud** 和 **Kubernetes** 用户提供类似 `cloudflared` 的内网穿透体验。

它通过动态调度 Kubernetes 资源（Deployments, Services, Ingresses），并利用双向多路复用 WebSocket 流（`yamux`）建立安全隧道，将你的本地开发环境直接暴露到公网。

## ✨ 特性

- 🔑 **无密码 OAuth2 登录**：使用设备授权流（Device Authorization Grant）通过 `sealtun login` 轻松连接。
- 🌍 **区域切换**：支持查看已内置的 Sealos Cloud region，并通过 `sealtun region use` 重新登录切换区域。
- 👤 **Profile 多账号管理**：可把不同 Sealos 账号、region、workspace 和 kubeconfig 保存为命名 profile，按需切换。
- 🚀 **一键暴露服务**：执行 `sealtun expose 8080`，即可获得一个受信任的 HTTPS URL，将流量安全地路由到本地。
- 🌐 **自定义域名**：新建隧道时可用 `--domain` 生成 CNAME 指引，并通过 `domain status/doctor` 检查 DNS、Ingress 与证书状态。
- 📊 **本地控制台与观测**：`dashboard` 提供本地 Web 控制台，`logs` 和 `metrics` 可查看远端 Pod 日志、请求计数与运行状态。
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

`make build` 默认会把当前 Git short hash 注入到本地二进制的 version 中，用于确认本地二进制和已 push 的代码提交一致。正式 tag 发布时，GitHub Actions 会用 tag 版本构建 GitHub Release 产物和容器镜像。

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

Sealtun 会自动执行以下操作：
1. 在你的 Sealos Namespace 中启动一个隧道代理 Pod。
2. 配置 Ingress 路由规则。
3. 建立加密 WebSocket 隧道，并将所有流量转发至 `localhost:3000`。

### 3. 使用自定义域名
新建隧道时先生成官方 Sealos 域名和 CNAME 目标：
```bash
sealtun expose 3000 --domain app.example.com

# 如果你会在命令等待期间配置 DNS，可以等待 CNAME 验证、绑定和证书就绪
sealtun expose 3000 --domain app.example.com --wait-domain
```

或者在 DNS 生效后对已有隧道绑定：
```bash
sealtun domain set <tunnel-id> app.example.com
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

### 4. 观测和本地控制台
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

`metrics` 会聚合本地 session 状态、远端 Deployment/Pod/Ingress 状态，并在远端 Pod 支持时读取受 Bearer secret 保护的 `/_sealtun/metrics` 请求计数。

启动本地只读控制台：
```bash
sealtun dashboard

# 自定义监听地址
sealtun dashboard --addr 127.0.0.1 --port 19777
```

Dashboard 仅监听本地地址，数据来自当前 CLI 进程读取到的本地 session、登录状态、远端诊断和自定义域名状态。

### 5. 声明式配置
创建 `sealtun.yaml`：
```yaml
version: v1
tunnels:
  - name: web
    localPort: 3000
    protocol: https
    domain: app.example.com
    waitDomain: false
    readyTimeout: 90s
    domainTimeout: 5m
```

应用配置：
```bash
# 离线校验和预览，不需要登录
sealtun apply -f sealtun.yaml --dry-run

# 创建或更新隧道
sealtun apply -f sealtun.yaml
```

`name` 会作为稳定 tunnel ID 使用，因此重复执行 `apply` 会更新同一个 `sealtun-<name>` 资源。自定义域名仍然遵循 CNAME 先验证再绑定的规则；新隧道如果 CNAME 未就绪，`apply` 会先保留 Sealos 官方域名并输出后续 `domain set` 指令；已有隧道则会拒绝未验证的自定义域名变更，避免误清理或覆盖正在使用的域名配置。

## 🛠️ 架构详情

- **底层协议**：基于 WebSocket 的 Yamux 多路复用。
- **Sealos 资源**：触发 `sealtun expose` 时，会在集群中创建以 `sealtun-*` 命名的 `Deployment`、`Service` 和 `Ingress`。
- **镜像来源**：依赖于 `ghcr.io/gitlayzer/sealtun` 的原生镜像。

## 🔧 当前已补强

- `expose` 现在会校验端口与协议参数，非法输入会在本地直接报错。
- `expose` 默认交给本地 daemon 后台维护；需要阻塞在当前终端时可使用 `--foreground`。
- 远端隧道 Pod 等待阶段增加了默认 `90s` 超时，可通过 `--ready-timeout` 调整。
- 配置目录统一为 `~/.sealtun`，首次运行只会迁移旧 `~/.sealos` 下的登录凭据和 kubeconfig，不会迁移旧 session 记录。
- `profile` 支持把多账号、多 region、多 workspace 登录状态保存为命名配置；`profile use` 会切换后续 `expose/status/region current` 使用的 active kubeconfig。
- Ingress 域名优先使用 Sealos Launchpad 返回的 `SEALOS_DOMAIN`，避免按 region 猜测公网域名。
- 自定义域名必须先通过 CNAME 归属验证，Sealtun 不会把未验证域名提前写入 Ingress，避免在共享 Ingress 中预占任意 Host。
- 绑定后会同时保留 Sealos 官方 host 和用户域名：daemon 始终使用 Sealos host 连接控制面，用户访问域名可通过 CNAME 指向该 Sealos host。
- `--wait-domain` 只在同时提供 `--domain` 时等待 DNS CNAME、绑定 Ingress 与 cert-manager 证书就绪；超时不会删除隧道，可稍后用 `sealtun domain set` 或 `sealtun domain verify` 复查。
- `domain status` 可批量查看所有自定义域名的 DNS、Ingress、证书是否就绪；`domain doctor` 会输出每个域名的详细诊断和告警。
- `logs` 读取远端 tunnel Pod 日志；`metrics` 聚合本地、远端和 server counters，其中 server counters 需要新版本远端镜像支持。
- `dashboard` 是本地只读 Web 控制台，不需要额外服务端组件。
- `apply -f sealtun.yaml` 是声明式配置 MVP，当前覆盖 HTTPS 隧道、稳定 tunnel name、自定义域名指引和 daemon 托管。
- 提供 `status`、`list`、`inspect`、`doctor`、`stop`、`cleanup`、`logout` 等本地控制命令。
- `list` 默认只读取本地 session；需要探测本地端口健康时可使用 `list --check`。
- `inspect` 默认展示本地状态；需要远端 Kubernetes 诊断时可使用 `inspect --remote`。
- `logout` 会先回收本地记录中的隧道资源再删除凭据；如果只想强制清除本地凭据，可使用 `logout --force`。
- 当前 `--protocol` 只接受 `https`。TCP、UDP 和 gRPC 泛化暂不支持，后续如果需要会以单独能力设计，而不是继续复用当前 HTTP Ingress 路径。
- `doctor` 会汇总本地 daemon、登录、session、端口健康和远端 Deployment、Service、Ingress、Pod 与 Event 状态，用于定位镜像拉取、Pod 未就绪、Ingress 缺失等问题。

## 📄 许可证

MIT License.
