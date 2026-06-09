# Sealtun Declarative Configuration

Use this for `sealtun.yaml`, `apply -f`, `diff -f`, multi-tunnel management, HTTPS access policy YAML, SSH tunnel declarations, and automatic expiration.

## Workflow

```bash
sealtun apply -f sealtun.yaml --dry-run
sealtun diff -f sealtun.yaml
sealtun apply -f sealtun.yaml
```

`--dry-run` validates and prints planned tunnels without login or cloud mutation. `diff` compares desired YAML with local sessions. Real `apply` requires login and creates or updates remote Kubernetes resources and local daemon sessions.

For first-time users, run or recommend:

```bash
sealtun status
sealtun login
sealtun apply -f sealtun.yaml --dry-run
sealtun diff -f sealtun.yaml
sealtun apply -f sealtun.yaml
```

If login opens an authorization flow, explain that the user must complete it before `apply` can create remote resources. Keep `--dry-run` and `diff` as the safe preview path before real cloud mutation.

## Declarative Decision Path

Use declarative config when the user wants repeatability, multiple tunnels, stable names, reviewable changes, TTL, or config they can keep in a project. Use one-shot `expose` when they only need a quick temporary tunnel.

| Need | YAML choice | Check |
| --- | --- | --- |
| Stable HTTPS tunnel | `protocol: https`, `name`, `localPort` | `apply --dry-run`, `diff`, then `inspect` |
| Public SSH | `protocol: ssh`, `localPort: 22` | output must show SSH host and port |
| Generic TCP/database | `protocol: tcp`, protocol-specific port | output must show `<host>:<port>` |
| Auto-expire | `ttl: 2h` or similar Go duration | verify `expiresAt` behavior in output/session |
| Secure HTTPS | `basicAuth` and/or `accessPolicy` with tokens, IP rules, rate limit, audit, or temporary links | prefer env-backed secrets unless local-only inline config is intentional |

Never add `domain`, `basicAuth`, or `accessPolicy` to `ssh` or `tcp` tunnels; those are HTTPS-layer features.

## Example

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
      rateLimit: 60/m
      audit:
        enabled: true
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

## Schema Notes

- `version` defaults to `v1`; only `v1` is supported.
- `tunnels` must contain at least one item.
- `name` is required, lower-case DNS-compatible, and becomes the stable tunnel ID. Reapplying the same name updates `sealtun-<name>`.
- Use `localPort`; `port` is accepted as a compatibility alias.
- `protocol` defaults to `https`; `ssh` is supported for direct TCP NodePort SSH, and `tcp` is supported for generic direct TCP NodePort tunnels. HTTP-only features such as `domain`, `basicAuth`, and `accessPolicy` are rejected for `ssh` and `tcp`.
- `ttl` uses Go duration syntax like `30m`, `2h`, or `24h`.
- `readyTimeout` and `domainTimeout` use Go duration syntax and must be positive.
- Multiple tunnels are applied in one run. On an apply failure, Sealtun attempts rollback for tunnels changed earlier in the batch.

## Basic Auth YAML

Inline credential:

```yaml
basicAuth:
  credential: admin:change-me
```

Expanded inline form:

```yaml
basicAuth:
  username: admin
  password: change-me
```

Environment-backed form:

```yaml
basicAuth:
  username: admin
  passwordEnv: SEALTUN_BASIC_AUTH_PASSWORD
```

Prefer `passwordEnv` for shared files. Use inline forms only when the user intentionally wants a fully self-contained local YAML file and understands the secret will be stored in that file.

## Access Policy YAML

```yaml
accessPolicy:
  bearerTokenEnv: SEALTUN_BEARER_TOKEN
  rateLimit: 60/m
  audit:
    enabled: true
  ipAllowlist:
    - 203.0.113.10
    - 198.51.100.0/24
  ipDenylist:
    - 198.51.100.9
  temporaryLinks:
    - name: review
      tokenEnv: SEALTUN_TEMP_TOKEN
      ttl: 1h
    - name: fixed-window
      token: local-only-token
      expiresAt: "2026-05-13T12:00:00Z"
```

Rules:

- `bearerToken` and `bearerTokenEnv` are mutually exclusive.
- Temporary links require `token` or `tokenEnv`, plus exactly one of `ttl` or `expiresAt`.
- `expiresAt` must be RFC3339 and in the future.
- Token values must be at least 8 characters.
- `sealtun apply` prints temporary URLs only when an inline `token` is present; `tokenEnv` avoids echoing the token.
- `rateLimit` uses fixed-window specs such as `60/m` or `1000/h`.
- `audit.enabled: true` enables HTTPS access audit. Audit records allow/deny reason and metadata, not plaintext secrets.

## SSH YAML

Use this when a user wants declarative public SSH over NodePort:

```yaml
version: v1
tunnels:
  - name: ssh-dev
    localPort: 22
    protocol: ssh
```

SSH declarations cannot set `domain`, `waitDomain`, `basicAuth`, or `accessPolicy`. The apply result should show the public SSH host, public SSH port, and direct `ssh <user>@<host> -p <port>` command.

Use this when a user wants declarative generic TCP:

```yaml
version: v1
tunnels:
  - name: postgres
    localPort: 5432
    protocol: tcp
```

TCP declarations cannot set `domain`, `waitDomain`, `basicAuth`, or `accessPolicy`. The apply result should show the public TCP host, public TCP port, and `<host>:<port>` endpoint.

## Domains In Declarative Apply

New tunnels with an unverified custom domain keep the generated Sealos host and print a warning with the later `sealtun domain set` command. Existing tunnels reject unverified custom-domain changes to avoid accidentally clearing or taking over live hostnames. Use `waitDomain: true` only when DNS is expected to become ready during the command.

## TTL Behavior

Tunnel `ttl` writes an `expiresAt` value into the local session. The local daemon deletes expired remote resources and local records. Reapplying the same `ttl` to a still-valid existing tunnel preserves the existing expiration instead of extending it on every apply.
