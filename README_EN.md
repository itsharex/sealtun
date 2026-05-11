# Sealtun

[中文版本](./README.md)

Sealtun is a powerful, elegant CLI tool that provides a `cloudflared` tunnel-like experience entirely built on **Sealos Cloud** and **Kubernetes**. 

It connects your local development machine straight to the internet by dynamically provisioning Kubernetes resources (Deployments, Services, Ingresses) and tunneling the traffic securely via bidirectional multiplexed WebSocket streams (`yamux`).

## Features

- 🔑 **Password-less OAuth2 Login**: Connect easily with `sealtun login` using the Device Authorization Grant flow.
- 🌍 **Region Switching**: List built-in Sealos Cloud regions and switch regions by re-running login with `sealtun region use`.
- 👤 **Named Profiles**: Save different Sealos accounts, regions, workspaces, and kubeconfigs as named profiles and switch between them.
- 🚀 **One-Command Expose**: Execute `sealtun expose 8080`, and get a fully trusted HTTPS URL for your localhost securely routed.
- 🌐 **Custom Domains**: Use `--domain` to print the required CNAME target and `domain status/doctor` to diagnose DNS, Ingress, and certificate readiness.
- 📊 **Local Console and Observability**: Use `dashboard` for a local web console, and `logs` / `metrics` for remote pod logs, request counters, and runtime state.
- 🧾 **Declarative Config**: Use `apply -f sealtun.yaml` to declare tunnels in YAML and create or update them with stable names.
- 🌐 **Optimized for Sealos**: Native support for Sealos Cloud domains, HTTPS traffic, and WebSocket tunnels.
- 🐳 **All-in-One Binary**: The client and the server agent live comfortably in the exact same compact binary and Docker image.
- ☸️ **Cloud-Native by Design**: Resources on Sealos are natively managed using standard Kubernetes API constructs.

## Installation / Setup

Install the `sealtun` CLI with npm, or download the binary for your platform from GitHub Releases. Remote tunnel Pods use the matching `ghcr.io/gitlayzer/sealtun` container image.

Install globally with npm:

```bash
npm install -g sealtun
sealtun --version
```

Run temporarily with npx:

```bash
npx sealtun@latest --version
npx sealtun@latest login
```

The npm package installs the matching platform-specific optional binary package automatically. It currently supports macOS, Linux, and Windows on `amd64/x64` and `arm64`.

Quick install for macOS / Linux:

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

Quick download for Windows PowerShell:

```powershell
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
Invoke-WebRequest -Uri "https://github.com/gitlayzer/sealtun/releases/latest/download/sealtun_windows_$arch.zip" -OutFile sealtun.zip
Expand-Archive .\sealtun.zip -DestinationPath .
.\sealtun.exe --version
```

For local development, build from source:

```bash
git clone https://github.com/gitlayzer/sealtun.git
cd sealtun
make build
./sealtun --version
```

`make build` injects the current Git short hash into the local binary version by default, which makes it easy to verify that the local binary matches the pushed commit. Tagged releases are built by GitHub Actions using the tag version for GitHub Release assets and container images.

## Release Process

Releases are tag-driven:

```bash
# 1. Test, commit, and push the branch first
go test ./...
make build
git push origin master

# 2. Then create and push a semantic version tag
git tag vX.Y.Z
git push origin vX.Y.Z
```

Pushing a `v*` tag triggers GitHub Actions: GoReleaser builds multi-platform binaries and creates the GitHub Release, while the Docker workflow builds and publishes the matching `ghcr.io/gitlayzer/sealtun` image. After release, run `make build && ./sealtun --version` again to confirm that the local binary reports the Git hash that was pushed.

## Quick Start

### 1. Login to Sealos
Perform the device authentication (which operates smoothly without passwords similar to `gh auth login`):
```bash
sealtun login

# List supported regions
sealtun region list

# Switch to another region
sealtun region use hzh

# Login and save credentials as a named profile
sealtun login gzg --profile gzg-main

# List and switch saved profiles
sealtun profile list
sealtun profile use hzh-dev
```
Built-in regions:

| Name | Region API | Ingress domain suffix |
| --- | --- | --- |
| `gzg` | `https://gzg.sealos.run` | `sealosgzg.site` |
| `hzh` | `https://hzh.sealos.run` | `sealoshzh.site` |
| `bja` | `https://bja.sealos.run` | `sealosbja.site` |
| `cloud` | `https://cloud.sealos.io` | `cloud.sealos.io` |
| `usw` | `https://usw-1.sealos.io` | `usw-1.sealos.app` |

*Note: Only built-in Sealos Cloud regions are currently supported. Login retrieves your Kubernetes credentials and the region's `SEALOS_DOMAIN`, then stores them under `~/.sealtun`. Named profiles are stored under `~/.sealtun/profiles/<name>`, and switching profiles replaces the active `auth.json` and `kubeconfig`.*

### 2. Expose a local port
For instance, to make your local Web Server running on Port `3000` accessible to everyone on the Internet:
```bash
# Default https protocol (compatible with WebSocket)
sealtun expose 3000

```

Sealtun will:
1. Spin up a tunnel proxy Pod in your Sealos namespace.
2. Establish the Ingress routes.
3. Automatically connect via WebSockets and proxy all L7 connections back to `localhost:3000`.

### 3. Use a custom domain
Create the tunnel first and print the Sealos-managed CNAME target:
```bash
sealtun expose 3000 --domain app.example.com

# If you will configure DNS while the command waits, verify CNAME, attach it, and wait for the certificate
sealtun expose 3000 --domain app.example.com --wait-domain
```

Or attach one to an existing tunnel after DNS is ready:
```bash
sealtun domain set <tunnel-id> app.example.com
```

Sealtun keeps a Sealos-managed host as the tunnel control endpoint and CNAME target. It writes the custom host to Ingress and creates cert-manager `Issuer` and `Certificate` resources only after the CNAME points to that Sealos host. Configure DNS at your provider:
```text
CNAME app.example.com -> <sealos-host>
```

Verify DNS, Ingress, and certificate readiness:
```bash
sealtun domain verify <tunnel-id>

# Keep waiting until DNS and certificate are ready or the timeout expires
sealtun domain verify <tunnel-id> --wait --timeout 5m

# Summarize every configured custom domain
sealtun domain status

# Run deeper diagnostics for one custom domain
sealtun domain doctor <tunnel-id>
```

Remove the custom domain:
```bash
sealtun domain clear <tunnel-id>
```

### 4. Observe tunnels and run the local dashboard
Show remote tunnel pod logs:
```bash
sealtun logs <tunnel-id>
sealtun logs <tunnel-id> --tail 200
sealtun logs <tunnel-id> --follow
```

Show tunnel metrics:
```bash
sealtun metrics <tunnel-id>
sealtun metrics <tunnel-id> --json
```

`metrics` combines local session state, remote Deployment/Pod/Ingress readiness, and server-side request counters when the remote pod supports the Bearer-secret-protected `/_sealtun/metrics` endpoint.

Run the local read-only dashboard:
```bash
sealtun dashboard

# Custom listen address
sealtun dashboard --addr 127.0.0.1 --port 19777
```

The dashboard listens locally and reads the same data as the CLI: local sessions, login state, remote diagnostics, and custom domain readiness.

### 5. Declarative config
Create `sealtun.yaml`:
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

Apply it:
```bash
# Offline validation and preview; no login required
sealtun apply -f sealtun.yaml --dry-run

# Create or update tunnels
sealtun apply -f sealtun.yaml
```

`name` is used as the stable tunnel ID, so repeated `apply` runs update the same `sealtun-<name>` resources. Custom domains still require verified CNAME ownership before attachment; for a new tunnel, `apply` keeps the Sealos-managed host and prints the follow-up `domain set` command when DNS is not ready. For an existing tunnel, `apply` rejects unverified custom-domain changes so it does not accidentally clear or overwrite a working domain configuration.

## Architecture Details

- **Protocol**: Yamux over Websocket.
- **Sealos Resources**: When you trigger `sealtun expose`, it creates `sealtun-*` variants of `Deployment`, `Service`, and `Ingress` in the active cluster context.
- **Images**: Relies on a single Docker image built natively targeting `ghcr.io/gitlayzer/sealtun`.

## Hardening Notes

- `expose` now validates port and protocol inputs before provisioning remote resources.
- `--protocol` currently supports only `https`. TCP, UDP, and gRPC are intentionally out of scope until there is a dedicated transport design for them.
- `profile` supports named login bundles for multiple accounts, regions, and workspaces; `profile use` switches the active kubeconfig used by later `expose`, `status`, and `region current` commands.
- Ingress host generation prefers the `SEALOS_DOMAIN` returned by Sealos Launchpad instead of guessing from the region host.
- Custom domains must pass CNAME ownership verification before Sealtun writes the custom host to Ingress, preventing unverified host preemption on shared Ingress controllers.
- After attachment, custom domains keep both hosts on the Ingress: the daemon uses the Sealos host for the control tunnel, while user traffic can use the CNAME-backed custom domain.
- `--wait-domain` waits for DNS CNAME, Ingress attachment, and cert-manager certificate readiness only when `--domain` is also provided; timeout does not delete the tunnel, and you can retry with `sealtun domain set` or recheck with `sealtun domain verify`.
- `domain status` summarizes DNS, Ingress, and certificate readiness for every custom domain; `domain doctor` prints detailed per-domain diagnostics and warnings.
- `logs` reads remote tunnel pod logs; `metrics` aggregates local state, remote readiness, and server counters when the remote image supports them.
- `dashboard` is a local read-only web console and does not require any additional hosted backend.
- `apply -f sealtun.yaml` is the declarative config MVP for HTTPS tunnels, stable tunnel names, custom domain guidance, and daemon-managed sessions.
- `list` reads local session records by default; use `list --check` to probe local target ports and report degraded sessions.
- `inspect` shows local session state by default; use `inspect --remote` to include best-effort Kubernetes diagnostics.
- `doctor` summarizes daemon, login, session, local port, and remote Deployment, Service, Ingress, Pod, and Event diagnostics.
- Tunnel pod readiness now has a default `90s` timeout, configurable via `--ready-timeout`.
- Configuration is stored in `~/.sealtun`; first run only migrates legacy auth and kubeconfig files from `~/.sealos`, not old tunnel session records.

## License

MIT License.
