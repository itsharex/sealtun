# Sealtun Troubleshooting

Use this when users report tunnel failures, auth issues, SSH/TCP connection failures, stale sessions, stop/start confusion, domain problems, missing metrics, or confusing CLI output.

## Fast Local Checks

```bash
sealtun status
sealtun region current
sealtun profile current
sealtun list --check
sealtun inspect <tunnel-id>
sealtun doctor
```

Start by checking login, active region/profile, local session records, and whether the local target port is reachable.

## Login, Region, Profile

Symptoms:

- `not logged in. Please run 'sealtun login' first`
- A tunnel appears in the wrong namespace or region.
- `apply` refuses to update a tunnel because region or namespace differs.
- First-time setup pauses while waiting for browser/device authorization.

Actions:

```bash
sealtun status --json
sealtun region list
sealtun region current
sealtun profile list
sealtun profile use <name>
sealtun login <region> --profile <name>
```

Profiles are stored under `~/.sealtun/profiles/<name>`. Switching a profile updates the active auth and kubeconfig used by later commands.

For first-time authorization, make the flow explicit: Sealtun needs Sealos authorization to obtain Kubernetes credentials for the active workspace. Ask the user to complete the browser/device flow, then verify with `sealtun status` before running `expose`, real `apply`, `domain set`, or cleanup operations.

## Daemon And Session Issues

Symptoms:

- Tunnel is listed but not connected.
- A stale tunnel remains after the owner process exits.
- Local daemon does not pick up an applied tunnel.

Actions:

```bash
sealtun list
sealtun inspect <tunnel-id>
sealtun doctor
sealtun doctor <tunnel-id>
sealtun stop <tunnel-id>
sealtun cleanup
```

`expose` normally starts daemon mode unless `--foreground` is used. `apply` also ensures the local daemon is running after successful cloud changes. `stop` intentionally preserves remote entry resources and scales the pod to zero; `start` or `resume` reopens it. `cleanup` deletes stopped, expired, or stale tunnels; `cleanup --all` force deletes all locally tracked tunnels.

## SSH Direct TCP Problems

Symptoms:

- `ssh <user>@<public-host> -p <node-port>` connects then closes.
- SSH works through ProxyCommand but not direct NodePort.
- User expects a normal HTTPS URL for an SSH tunnel.

Actions:

```bash
sealtun inspect <tunnel-id> --remote
sealtun logs <tunnel-id> --tail 200
sealtun list --check
ssh -vvv <user>@<public-host> -p <node-port>
```

Direct SSH uses `--protocol ssh` and a public TCP NodePort. The public host plus NodePort is the user-facing entry; HTTPS is only the internal Sealtun control channel. Basic Auth, Bearer tokens, temporary links, IP policies, and custom domains do not apply to SSH. If the SSH debug log reaches authentication and then closes, inspect the local sshd authentication policy, user, keys/password, and PAM/keyboard-interactive behavior on the machine running Sealtun.

For generic TCP, use `--protocol tcp` and connect to `<public-host>:<node-port>` with the protocol-specific client, for example `psql -h <public-host> -p <node-port>`. If the connection opens and closes, inspect the local target service logs and authentication settings on the machine running Sealtun.

## Local Port Unreachable

Symptoms:

- Public URL returns the Sealtun offline page.
- `list --check` shows degraded local target health.
- `inspect` shows the tunnel owner alive but target port unreachable.

Actions:

```bash
sealtun discover
sealtun list --check
sealtun inspect <tunnel-id>
lsof -i :3000
curl -v http://127.0.0.1:3000/
```

Use `sealtun discover` when the user is unsure which local port is actually listening; it scans local TCP listening ports only and provides protocol/template hints without creating a tunnel. Fix the local service first. Sealtun forwards to `localhost:<localPort>` from the machine running the CLI.

## Remote Kubernetes Or Pod Problems

Symptoms:

- Timed out waiting for tunnel server.
- Remote pod is not ready.
- Image pull, service, or ingress errors.

Actions:

```bash
sealtun doctor <tunnel-id>
sealtun inspect <tunnel-id> --remote
sealtun logs <tunnel-id> --tail 200
sealtun metrics <tunnel-id> --json
sealtun events <tunnel-id>
sealtun doctor --json
```

Remote diagnostics inspect the Sealtun-managed Deployment, Service, Ingress, Pod, Events, and readiness where available. If code changes are needed, inspect `pkg/k8s`, `cmd/inspect.go`, `cmd/doctor.go`, and related tests.
Prefer `sealtun doctor <tunnel-id>` before asking the user to manually inspect Kubernetes. It summarizes local owner state, local port reachability, remote resource readiness, and next-step suggestions.

In dashboard, use the Resources tab for a read-only resource list and resource occupancy hints. It shows Deployment, Pod, HTTP Service, TCP NodePort Service, Ingress, Certificate, Issuer, and Secret metadata, but it does not estimate cloud billing and never displays Secret data.

## Custom Domain, DNS, Certificate

Symptoms:

- Custom domain still points to the Sealos host only.
- `domain set` or `apply` refuses an unverified domain.
- Certificate is not ready.

Actions:

```bash
sealtun domain status
sealtun domain plan <tunnel-id> app.example.com
sealtun domain add <tunnel-id> app.example.com --wait --timeout 5m
sealtun domain verify <tunnel-id>
sealtun domain verify <tunnel-id> --wait --timeout 5m
sealtun domain doctor <tunnel-id>
```

Confirm the user configured:

```text
CNAME <custom-domain> -> <sealos-host>
```

Sealtun intentionally verifies the CNAME before writing the custom host to Ingress. This avoids claiming arbitrary hosts in shared Ingress infrastructure. Certificate readiness depends on cert-manager after the custom host is attached.
Use `domain plan` for non-mutating DNS guidance. Use `domain add --wait` only when the user explicitly wants to bind the domain and wait for readiness.

## Access Control Problems

Symptoms:

- Basic Auth prompt is missing or rejects credentials.
- Bearer token requests return unauthorized.
- Temporary link stopped working.
- IP allowlist denies a caller unexpectedly.

Actions:

```bash
sealtun inspect <tunnel-id>
sealtun logs <tunnel-id> --tail 200
sealtun metrics <tunnel-id> --json
```

Check the session access policy. Tokens must be at least 8 characters. Temporary links expire at `expiresAt`; query parameter name is `_sealtun_token`. IP decisions use `X-Real-IP`, then the last valid proxy-confirmed client IP in `X-Forwarded-For`, then `RemoteAddr`, so upstream proxy headers matter.

## Metrics And Dashboard

`metrics` combines local session data, remote Kubernetes readiness, and server counters where the remote image supports them. Missing server counters usually mean the remote pod is older or unreachable, not necessarily that the tunnel is down.

Dashboard is a local workbench by default:

```bash
sealtun dashboard --addr 127.0.0.1 --port 19777
```

It can create tunnels, run YAML dry-run/diff/apply, stop/start/cleanup tunnels, show logs/metrics/events, and manage custom domains for the current active profile/region/namespace. Mutating actions require both the dashboard token and a confirmation value. Treat `dashboard --allow-remote` as exposing local operational control, not just read-only data; remote mode does not embed the token in HTML.
