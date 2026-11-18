# Security Policy

## Supported Versions

| Version | Supported |
|---|---|
| `1.0.x` (and `main` / pre-release candidate) | Yes |
| `< 1.0` prototypes | No |

## Reporting a Vulnerability

Please **do not** open a public GitHub issue with exploit details, private endpoints, or proof-of-concept payloads for suspected security vulnerabilities.

Instead, report vulnerabilities privately to the repository maintainers via GitHub Security Advisories (`Security -> Advisories -> Report a vulnerability` on <https://github.com/dgulan/YLReader>) with:

- Affected component (`backend/cmd/server`, `backend/internal/engine/sandbox`, `backend/internal/engine/fetcher`, `backend/cmd/migrator`, `frontend`, or deployment manifests)
- Reproduction steps, configuration conditions, and expected vs. observed behavior
- Potential impact (for example: SSRF bypass, sandbox escape/exhaustion, header credential leak, or privilege escalation)

We aim to acknowledge reports within 72 hours, confirm severity and remediation scope within 7 days, and coordinate disclosure once a fix and verification tests are ready.

---

## Security Architecture & Defensive Boundaries

YLReader executes user-imported third-party Legado book source rules and serves multi-device self-hosted reading sessions. The platform enforces defense-in-depth across four layers:

### 1. JavaScript Rule Sandbox Isolation (`goja` & `sandboxworker`)

- **Pure-Go VM without CGO/V8**: Book source JS runs in `dop251/goja` with no host OS, filesystem, shell, or arbitrary network modules. Only explicit whitelisted `java.*` and `source.*` bridge helpers are exposed.
- **Bounded Execution & Memory Budgets**:
  - Per-execution script deadline (`DefaultScriptTimeout = 3s`), script size cap (`DefaultScriptLimit = 256 KiB`), output size cap (`DefaultOutputLimit = 1 MiB`), and AST/value depth guards.
  - Configurable process-wide limits (`YREADER_SANDBOX_INPUT_LIMIT_MB` 1..32 MiB, `YREADER_SANDBOX_INFLIGHT_MB` >= 2x InputLimit, `YREADER_SANDBOX_MAX_CONCURRENT` 1..32; invalid values fail closed at startup).
- **OS/Container-Isolated Workers (`docker-compose.prod.yml`)**:
  - Production deployments run `cmd/sandboxworker` as dedicated `network_mode: "none"` containers with `read_only: true`, non-root `10001:10001`, `cap_drop: ["ALL"]`, `no-new-privileges:true`, default seccomp, `mem_limit: 256m` (no swap), `pids_limit: 64`, bounded CPU, and private `0700` Unix socket IPC directories.
  - Workers self-verify cgroup v2, capability, `NoNewPrivs`, Seccomp, read-only rootfs, and loopback-only network constraints on startup and refuse to boot if any guard is weakened.
  - The API process never falls back to in-process JS when `YREADER_SANDBOX_SOCKETS` is configured and workers are unreachable.

### 2. SSRF & Outbound HTTP Defense (`internal/engine/fetcher`)

- **Scheme & Target Validation**: Only `http` and `https` schemes are permitted for book-source outbound requests.
- **Dial-Time IP Verification & DNS Rebinding Protection**: `Fetcher` validates both literal IPs and DNS-resolved addresses right before TCP dialing inside `DialContext` (unmapping IPv4-mapped IPv6 addresses first) and rejects:
  - Non-global unicast, loopback (`127.0.0.0/8`, `::1`), RFC 1918 private networks (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), IPv6 ULA (`fc00::/7`), unspecified, and multicast addresses
  - Link-local & cloud metadata ranges (`169.254.0.0/16`, `fe80::/10`)
  - Carrier-grade NAT (`100.64.0.0/10`), benchmarking (`198.18.0.0/15`), NAT64, 6to4, Teredo, and documentation ranges
- **Redirect & Header Hardening**:
  - Redirect chains are capped (`MaxRedirects = 5`), re-checked on every hop, and automatically strip sensitive headers (`Authorization`, `Cookie`) on cross-host redirects.
  - Static source `header` maps undergo CR/LF/NUL control-character rejection and whitelist filtering; hop-by-hop and framing headers (`Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Proxy-*`) cannot be overridden.
- **Response & Rate Guards**: Domain/source rate limiting (`DomainLimiter` / Redis admission) and strict response body size caps (`DefaultMaxBodyBytes = 16 MiB`) protect against upstream resource amplification.
- **Container Network Segmentation**: In `docker-compose.prod.yml`, PostgreSQL and Redis attach strictly to an `internal: true` Docker network with no host port exposure or internet egress.

### 3. Credential, Session, and Persistence Security

- **Password & Token Hygiene**: User passwords are hashed with Argon2id (`m=65536, t=3, p=2`, 16-byte salt, 32-byte key). Access tokens are short-lived JWTs paired with revocable refresh sessions and `token_version` invalidation.
- **Production Fail-Closed Secrets**: When `ENVIRONMENT=production`, `cmd/server` refuses to start unless `JWT_SECRET` contains at least 32 bytes and a valid `YREADER_HEADER_KEY_<KID>` keyring (32-byte AES-GCM key) is loaded.
- **Book Source Header Envelope Encryption**: Static `book_sources.header` credentials are encrypted at rest with AES-256-GCM bound to `AAD = user_id|source_id`. `migrator import-legado-sources` requires a configured keyring so imported source headers are encrypted before hitting disk.
- **Diagnostic Sanitization**: Operational failure logs (`source_failures.message`), migration guards, and CLI error messages avoid recording DSN credentials, raw secret headers, or full authenticated upstream URLs.

### 4. Web Frontend & Offline PWA Boundaries

- **Strict CSP & Security Headers (`frontend/nginx.conf`)**: `default-src 'self'`, `script-src 'self'`, `frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`. Deploy behind a TLS-terminating reverse proxy sending `Strict-Transport-Security`.
- **Service Worker Scope (`frontend/public/sw.js`)**: Caches only build-time same-origin static shell assets (`/`, `/index.html`, `/manifest.webmanifest`, `/assets/*`). All `/api/*` endpoints, non-`GET` methods, cross-origin requests, `no-store` responses, and `Set-Cookie` responses are explicitly excluded from Cache Storage.
- **Owner-Scoped Client Cache**: Offline chapters and reader preferences reside in IndexedDB scoped by authenticated user owner and are cleared on logout or account switch.

---

## Operator Checklist

1. Generate independent random values for `JWT_SECRET`, `YREADER_HEADER_KEY_primary`, `POSTGRES_PASSWORD`, and `REDIS_PASSWORD` in `.env`. Never deploy with `scripts/compose-check.env` values.
2. Back up and store `YREADER_HEADER_KEY_<KID>` safely across restarts and restores; encrypted source headers cannot be recovered without the matching KID key.
3. Run a TLS-terminating reverse proxy (HTTPS + HSTS) in front of the loopback `frontend` port in `docker-compose.prod.yml`, and clear/rotate `SEED_PASSWORD` once initial admin provisioning completes.
4. Always back up data before running `migrator schema up --production-confirm-db=<dbname>`, and restrict `/metrics` to trusted monitoring networks.
