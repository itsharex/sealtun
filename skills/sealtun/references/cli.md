# Sealtun CLI Reference

Use this for interactive Sealtun operation: install, shell completion, login, expose HTTPS, SSH, or generic TCP, secure public HTTP traffic, observe, bind domains, stop/start, and clean up tunnels.

## Install

```bash
npm install -g sealtun
sealtun --version

npx sealtun@latest --version
npx sealtun@latest login
```

Direct binaries are published on GitHub Releases. The npm package installs a platform-specific optional binary package for macOS, Linux, or Windows on x64/amd64 and arm64.

## Shell Completion

```bash
sealtun completion bash
sealtun completion zsh
sealtun completion fish
sealtun completion powershell
```

Use the generated script according to the user's shell. If the user only asks whether completion exists, show the matching command instead of editing shell startup files.

## Login, Regions, Profiles

```bash
sealtun login
sealtun status
sealtun region list
sealtun region current
sealtun region use hzh

sealtun login gzg --profile gzg-main
sealtun profile list
sealtun profile current
sealtun profile save hzh-dev
sealtun profile use hzh-dev
sealtun profile delete hzh-dev
```

Known regions include `gzg`, `hzh`, `bja`, `cloud`, and `usw`. Login state, kubeconfig, and profiles live under `~/.sealtun`.

First-use behavior:

- Before creating cloud resources, check `sealtun status` when feasible.
- If the user is not logged in, explain that `sealtun login` opens a Sealos authorization flow and stores the resulting auth/kubeconfig under `~/.sealtun`.
- If a browser/device authorization flow opens, wait for the user to finish it. Do not retry repeatedly while the user is authorizing.
- After login, verify with `sealtun status`, `sealtun region current`, and `sealtun profile current` when profiles are involved.
- For multiple accounts, regions, or workspaces, prefer `sealtun login <region> --profile <name>` and `sealtun profile use <name>` instead of overwriting the active login without explanation.

## Expose A Port

```bash
sealtun expose 3000
sealtun expose 3000 --foreground
sealtun expose 3000 --ready-timeout 2m
```

`expose` defaults to `https` and daemon mode. The daemon maintains the local side in the background. Use `--foreground` when the current terminal should own the tunnel lifecycle.

Use `https` when the user wants a browser URL, webhook callback URL, OAuth callback, payment callback, public preview link, Basic Auth, Bearer tokens, temporary access links, IP allowlist/denylist, or custom domain.

## Public Access Controls

Access controls are enforced by the Sealtun server proxy layer, independent of Ingress annotations. They apply to public business traffic, not `/_sealtun/ws`, health checks, or internal metrics protected by the tunnel secret.

Prefer environment variables for credentials:

```bash
export SEALTUN_BASIC_AUTH_PASSWORD='change-me'
sealtun expose 3000 \
  --basic-auth-user admin \
  --basic-auth-password-env SEALTUN_BASIC_AUTH_PASSWORD

export SEALTUN_BEARER_TOKEN='share-secret'
sealtun expose 3000 --bearer-token-env SEALTUN_BEARER_TOKEN

sealtun expose 3000 \
  --ip-allowlist 203.0.113.10,198.51.100.0/24 \
  --ip-denylist 198.51.100.9

export SEALTUN_TEMP_TOKEN='review-link-secret'
sealtun expose 3000 \
  --temporary-access-token-env SEALTUN_TEMP_TOKEN \
  --temporary-access-ttl 1h
```

One-shot forms exist, but warn that they can enter shell history:

```bash
sealtun expose 3000 --basic-auth admin:change-me
sealtun expose 3000 --bearer-token share-secret
sealtun expose 3000 --temporary-access-token review-link-secret --temporary-access-ttl 1h
```

Token constraints and behavior:

- Bearer and temporary tokens must be at least 8 characters.
- Stored runtime policy uses SHA-256 token hashes.
- Temporary access uses `?_sealtun_token=<token>` and strips that query parameter before forwarding upstream.
- IP rules accept individual IPs or CIDR ranges. Sealtun reads `X-Real-IP`, then the last valid proxy-confirmed client IP in `X-Forwarded-For`, then `RemoteAddr`.
- When Basic Auth and Bearer or temporary links are both configured, either authentication path can allow the request, subject to IP rules.

## Custom Domains

```bash
sealtun expose 3000 --domain app.example.com
sealtun expose 3000 --domain app.example.com --wait-domain --domain-timeout 5m

sealtun domain plan <tunnel-id> app.example.com
sealtun domain add <tunnel-id> app.example.com
sealtun domain add <tunnel-id> app.example.com --wait --timeout 5m
sealtun domain set <tunnel-id> app.example.com
sealtun domain verify <tunnel-id>
sealtun domain verify <tunnel-id> --wait --timeout 5m
sealtun domain status
sealtun domain doctor <tunnel-id>
sealtun domain clear <tunnel-id>
```

Sealtun keeps a generated Sealos host as the control-plane host and CNAME target. The user must configure:

```text
CNAME app.example.com -> <sealos-host>
```

Only after CNAME ownership verification does Sealtun write the custom host to Ingress and manage cert-manager resources.

Prefer `domain plan` when the user only needs DNS guidance. Use `domain add --wait` when the user explicitly wants Sealtun to wait for DNS, attach the domain, and wait for certificate readiness. `domain set` remains the direct attach command when DNS is already known to be ready.

## Protocol Templates

```bash
sealtun template https --name web --port 3000 --domain app.example.com
sealtun template ssh
sealtun template tcp --name debug --port 9000
sealtun template mysql
sealtun template postgres
sealtun template redis --name cache
sealtun template mqtt
```

Use templates when the user asks how to expose a common protocol or wants a starter `sealtun.yaml`. Templates are read-only and print both a one-shot `sealtun expose` command and a YAML snippet. `mysql`, `postgres`, `redis`, and `mqtt` map to generic `tcp`; only `https` templates accept `--domain`.

## SSH Over Sealtun

For regions that support public TCP NodePort, prefer direct L4 SSH:

```bash
sealtun expose 22 --protocol ssh
ssh <user>@<public-host> -p <node-port>
```

`--protocol ssh` exposes only a public TCP NodePort for user traffic. HTTPS is kept only as the internal control channel used by the local daemon, not as a default application URL. Basic Auth, Bearer tokens, temporary links, IP policies, and custom domains are HTTP-layer features and are rejected for SSH tunnels.

Use SSH mode only when the user wants to expose a local SSH server or direct TCP SSH entry. It prints `Public SSH host`, `Public SSH port`, and an `ssh <user>@<public-host> -p <node-port>` command. Do not promise a custom domain for SSH; users connect with the generated host plus NodePort.

When direct NodePort is unavailable, use the WebSocket ProxyCommand fallback:

```bash
sealtun expose 22
ssh -o ProxyCommand='sealtun ssh connect <tunnel-id>' <user>@sealtun
```

`sealtun ssh connect <tunnel-id>` opens `wss://<sealos-host>/_sealtun/tcp` with the tunnel's internal secret, then bridges stdin/stdout to the remote server's active yamux session.

## Generic TCP Over Sealtun

For non-HTTP protocols such as databases, queues, or debugging services, use generic L4 TCP:

```bash
sealtun expose 5432 --protocol tcp
```

The command prints `Public TCP host`, `Public TCP port`, and `Public TCP endpoint` as `<public-host>:<node-port>`. Basic Auth, Bearer tokens, temporary links, IP policies, and custom domains are HTTPS proxy-layer features and are rejected for TCP tunnels.

## Observe And Manage

```bash
sealtun status
sealtun status --json

sealtun list
sealtun list --check
sealtun list --json

sealtun inspect <tunnel-id>
sealtun inspect <tunnel-id> --remote
sealtun inspect <tunnel-id> --json

sealtun logs <tunnel-id>
sealtun logs <tunnel-id> --tail 200 --follow
sealtun logs <tunnel-id> --since 10m

sealtun metrics <tunnel-id>
sealtun metrics <tunnel-id> --json

sealtun events <tunnel-id>
sealtun events <tunnel-id> --json

sealtun dashboard
sealtun dashboard --addr 127.0.0.1 --port 19777

sealtun doctor
sealtun doctor <tunnel-id>
sealtun doctor --json
sealtun doctor <tunnel-id> --json
```

Dashboard is local and read-only by default. `--allow-remote` allows a non-loopback dashboard address and should be treated as a security-sensitive choice.

Use `doctor <tunnel-id>` for "why can't I connect" issues. It checks the local session, owner process or daemon, local target port, remote resources where credentials are available, and prints next-step suggestions.

## Stop And Clean Up

```bash
sealtun stop <tunnel-id>
sealtun start <tunnel-id>
sealtun resume <tunnel-id>
sealtun cleanup
sealtun cleanup --all
sealtun logout
sealtun logout --force
```

`stop` scales the remote tunnel Deployment to zero and keeps the domain, Service, Ingress, secrets, NodePort Service for SSH, and local session record. Use `start` or its `resume` alias to scale the same tunnel back up and reconnect it through the local daemon.

`cleanup` deletes stopped, expired, or stale tunnels and removes their local session records. `cleanup --all` force deletes every locally tracked tunnel, including active ones, and should be used only when you intentionally want to remove all tracked remote resources.

`logout` first tries to clean up locally tracked tunnel resources before deleting credentials. Use `--force` only when cleanup cannot complete and local credentials must be removed anyway.
