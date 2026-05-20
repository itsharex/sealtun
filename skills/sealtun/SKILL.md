---
name: sealtun
description: "Use this skill for Sealtun CLI usage, local-to-public tunnels, and Sealtun troubleshooting. Trigger for sealtun, sealtun.yaml, Sealos tunnel, ngrok/cloudflared-style tunnel, expose localhost/local port/local dev server, public HTTPS URL/domain for local app, public SSH/TCP tunnel, NodePort SSH, ProxyCommand fallback, webhook/payment/OAuth/bot callback to local service, preview/demo link, custom domain/CNAME, domain plan/add/verify/status/doctor, Basic Auth, Bearer token, IP allowlist/denylist, temporary access links, ttl auto-expire, apply/diff multi-tunnel config, protocol templates for https/ssh/tcp/mysql/postgres/redis/mqtt, stop/start/resume, cleanup, daemon/session/logs/events/metrics/dashboard/doctor. Chinese triggers: 内网穿透, 本地服务公网访问, 本地端口暴露, localhost 暴露到公网, 公网预览链接, 公网域名, 自定义域名绑定, CNAME 指引, 公网 SSH, SSH 隧道, TCP 隧道, MySQL/Postgres/Redis/MQTT 公网访问, 第三方回调到本地, 隧道认证, 访问控制, 声明式配置, 协议模板, 停止隧道, 清理隧道, 隧道诊断, 隧道日志, 隧道指标, 隧道事件. Do not use for generic Kubernetes/Ingress/DNS/SSH unless Sealtun is involved."
---

# Sealtun

## First Decision

Classify the request before answering or editing:

- User operation: install, shell completion, login, expose HTTPS, SSH, or generic TCP, generate protocol templates, secure public HTTP traffic, plan/add/verify a custom domain, inspect state, stop/start/resume, clean up, or use the dashboard. Read `references/cli.md`.
- Declarative configuration: `sealtun.yaml`, `apply -f`, `diff -f`, multi-tunnel management, stable names, `ttl`, HTTPS access policies, SSH tunnel declarations, or generic TCP tunnel declarations. Read `references/declarative.md`.
- Troubleshooting: login/profile mismatch, daemon/session issues, local port failures, SSH/TCP direct NodePort problems, remote Kubernetes problems, DNS, Ingress, certificate, logs, metrics, events, or dashboard behavior. Read `references/troubleshooting.md`.

If the request is inside the Sealtun repository, prefer the current source tree and README over these references when they conflict. Use `rg` to inspect Cobra commands and flags before changing CLI usage guidance.

## Required Execution Flow

Follow this flow after the skill triggers:

1. Scope gate: verify the request is about making a local/dev service publicly reachable, operating a Sealtun tunnel, troubleshooting Sealtun, or declarative Sealtun config. If it is only generic production deployment, buying a domain, DNS-only configuration, generic Kubernetes, or generic SSH without Sealtun tunneling, do not force Sealtun into the answer.
2. Select one mode before acting:
   - Guidance mode: user asks how to use Sealtun. Load the matching reference and give commands; do not run live tunnel/cloud commands.
   - Live operation mode: user explicitly asks to execute, create, apply, stop, clean up, or bind a domain. Run preflight checks first, then the requested command, then verification.
   - Troubleshooting mode: user reports a problem. Run non-mutating diagnostics first, identify the likely layer, then propose or perform fixes only when the requested action is clear.
3. Gather minimum context. Inside this repo, inspect current code/README before relying on references. Outside the repo, use the references as the command source. Prefer non-mutating checks such as `sealtun --version`, `sealtun status`, `sealtun profile current`, `sealtun region current`, `sealtun list`, `sealtun inspect`, and `sealtun doctor`.
4. Handle first-use authorization gently. If `status` shows no login, explain that Sealtun needs Sealos authorization and kubeconfig before creating cloud resources. Guide the user through `sealtun login`, or `sealtun login <region> --profile <name>` when region/profile matters. If a browser/device authorization flow opens, tell the user to complete it in the browser and wait; do not treat the pause as a failure. After login, verify with `sealtun status`, `sealtun region current`, and optionally `sealtun profile current`.
5. Control mutations. Do not run `sealtun expose`, `sealtun apply`, `sealtun domain add/set/clear`, `sealtun stop`, `sealtun cleanup`, or `sealtun logout` unless the user explicitly asked for that operation in the current task. `sealtun template`, `sealtun domain plan`, and read-only diagnostics are safe guidance steps. For declarative changes, prefer `apply --dry-run` and `diff` before real `apply`.
6. Verify completion. After live operations, inspect the resulting tunnel/session/domain state. Report the exact command sequence and final state, without printing secrets.

## Operating Rules

- Do not expose user secrets in final answers, logs, commits, or generated docs. Prefer `*Env` fields and environment variables for passwords and tokens unless the user explicitly wants a one-shot inline example.
- Explain that Sealtun public access controls are enforced in the Sealtun server proxy layer, not by Ingress annotations. They protect HTTPS public business traffic, not the internal `/_sealtun/ws` control channel and not SSH/TCP direct NodePort traffic.
- For SSH exposure, prefer `sealtun expose 22 --protocol ssh` when the region supports public TCP NodePort. Use `sealtun ssh connect <tunnel-id>` only as a WebSocket ProxyCommand fallback.
- For generic TCP exposure, prefer `sealtun expose <port> --protocol tcp` and report the generated `<public-host>:<node-port>` endpoint.
- For declarative work, run or recommend `sealtun apply -f sealtun.yaml --dry-run` and `sealtun diff -f sealtun.yaml` before a real apply when feasible.
- For first-time users, prioritize a clear path: install, `sealtun login`, confirm region/profile, then create or apply a tunnel. Mention that login stores credentials under `~/.sealtun` and that profiles are useful for multiple Sealos accounts, regions, or workspaces.
- Use exact command names and flags from the repository when modifying instructions. Supported tunnel protocols are `https`, dedicated `ssh`, and generic `tcp`; UDP/gRPC are not supported unless the repo adds them.

## Response Shape

For usage questions, give a short working command sequence and explain only the relevant gotchas. For troubleshooting, start with the lowest-cost local checks, then escalate to remote Kubernetes diagnostics.
