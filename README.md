# TunnelX — self-hosted HTTP development tunnel

TunnelX is an advanced, source-available prototype for exposing a local HTTP service through a public VPS. It includes a Go server, Go agent, authenticated dashboard, live tunnel status, recent request logs, JSONL log persistence, Docker packaging, and Caddy HTTPS deployment guidance. It forwards real requests; it does not simulate tunnel traffic.

> **Project status:** development/MVP, not a hardened, multi-tenant production replacement for ngrok. It uses a shared server-agent token and path-based public routes (`/t/<name>/`). There are no customer accounts, billing, custom per-user domains, persistent database, WebSocket passthrough, or streaming bodies yet. Review the security notes before exposing it to the public internet.

## Features

- Go server and Go client/agent, using an authenticated outbound WebSocket.
- HTTP request and response forwarding to a local HTTP/HTTPS target.
- Multiple named tunnels on one server, with automatic agent reconnect.
- Password-protected dashboard and authenticated JSON APIs using HTTP Basic Auth.
- Active tunnel list and recent request table, refreshing every five seconds.
- Request logs persisted as JSON Lines under `TUNNELX_DATA_DIR/requests.jsonl`; dashboard keeps the latest 500 records in memory.
- Health endpoint (`GET /healthz`) and metrics endpoint (`GET /api/metrics`, admin auth required).
- Request size limits (1 MiB), request timeout, security response headers, and restrictive WebSocket Origin check.
- Windows PowerShell build/demo helpers, Dockerfile, Compose file, Caddy configuration, and systemd unit.

## Architecture

```text
Browser / API client
       | HTTPS
       v
Domain + Caddy (public ports 80/443)
       | reverse proxy to localhost:8080
       v
TunnelX Go server  <==== authenticated persistent WebSocket ====>  TunnelX Go agent
       |                                                              |
       +-- dashboard, status, logs                                    v
                                                           localhost target app
```

The agent opens an outbound connection to the VPS, so your development laptop does not need an inbound port open. Public traffic is routed using a path prefix: `https://your-domain.example/t/demo/`.

## Requirements

- Windows 11 or Linux/macOS for the Go client; Go 1.22+ (tested target Go 1.23).
- Network access to download `github.com/gorilla/websocket` the first time you build.
- For public access: a Linux VPS with a public IP, a domain/subdomain with DNS pointing to that VPS, and inbound TCP ports 80/443 open for Caddy. Keep the Go server port 8080 private to localhost or a private network.

## Windows 11 — local end-to-end test

### 1. Install Go

Install Go from <https://go.dev/dl/>. Open a fresh PowerShell and verify:

```powershell
go version
```

Extract the ZIP and open PowerShell in the `tunnelx` repository folder.

### 2. Download dependency and build

```powershell
go mod tidy
.\deploy\scripts\build-windows.ps1
```

If PowerShell blocks scripts, run `Set-ExecutionPolicy -Scope Process Bypass` in that terminal only, then retry. If dependency download fails, check network/DNS; the source package does not vendor third-party dependencies.

### 3. Start server (Terminal 1)

Use strong unique secrets. The token must be the same on server and client and at least 32 characters long.

```powershell
$env:TUNNELX_TOKEN = 'replace-with-a-long-random-secret-at-least-32-chars'
$env:TUNNELX_ADMIN_USER = 'admin'
$env:TUNNELX_ADMIN_PASS = 'replace-with-a-strong-password'
$env:TUNNELX_LISTEN = '127.0.0.1:8080'
$env:TUNNELX_DATA_DIR = '.\data'
go run .\cmd\server
```

Open `http://127.0.0.1:8080/`. Browser will prompt for dashboard username/password. For local testing only, HTTP is fine; never expose the admin dashboard directly over public plain HTTP.

### 4. Start a sample local web app (Terminal 2)

```powershell
New-Item -ItemType Directory -Force C:\temp\tunnelx-demo
Set-Content C:\temp\tunnelx-demo\index.html 'TunnelX local app is working'
Set-Location C:\temp\tunnelx-demo
python -m http.server 8000
```

### 5. Start agent (Terminal 3)

From the TunnelX repository folder:

```powershell
$env:TUNNELX_TOKEN = 'replace-with-a-long-random-secret-at-least-32-chars'
go run .\cmd\client --server ws://127.0.0.1:8080/_tunnel/agent --name demo --target http://127.0.0.1:8000 --token $env:TUNNELX_TOKEN
```

Open `http://127.0.0.1:8080/t/demo/`. It should show `TunnelX local app is working`. Refresh the dashboard and confirm the request appears in Recent requests. The sample server is plain HTTP and should only be used for this local test.

## Public VPS deployment (Ubuntu example)

1. Create a VPS, point an A record such as `tunnelx.example.com` to its public IPv4, and allow TCP 80/443 in the provider firewall and OS firewall.
2. Install Go on the VPS or build the Linux server binary on a compatible build machine. Copy `dist/tunnelx-server` to `/usr/local/bin/tunnelx-server`.
3. Create a locked-down service user and data directory:

```bash
sudo useradd --system --home /opt/tunnelx --shell /usr/sbin/nologin tunnelx
sudo install -d -o tunnelx -g tunnelx -m 0700 /var/lib/tunnelx
sudo install -d -m 0700 /etc/tunnelx
sudo install -m 0600 /dev/null /etc/tunnelx/tunnelx.env
sudo nano /etc/tunnelx/tunnelx.env
```

Put `TUNNELX_TOKEN`, `TUNNELX_ADMIN_USER`, and `TUNNELX_ADMIN_PASS` in `/etc/tunnelx/tunnelx.env`. Use a cryptographically random token of at least 32 characters and a unique strong admin password. Do not commit these secrets.

4. Copy `deploy/tunnelx.service` to `/etc/systemd/system/tunnelx.service`, then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tunnelx
sudo systemctl status tunnelx
```

5. Install Caddy using its official instructions: <https://caddyserver.com/docs/install>. Copy `deploy/Caddyfile` to Caddy's configuration location and replace the example hostname. Configure Caddy to proxy to `127.0.0.1:8080`, then validate and reload Caddy. Caddy can provision/renew TLS certificates when DNS and ports are correct.
6. From your Windows machine, connect the agent using the public TLS WebSocket URL:

```powershell
$env:TUNNELX_TOKEN = 'same-secret-as-server'
.\dist\tunnelx.exe --server wss://tunnelx.example.com/_tunnel/agent --name demo --target http://127.0.0.1:8000 --token $env:TUNNELX_TOKEN
```

7. Public URL: `https://tunnelx.example.com/t/demo/`. Dashboard: `https://tunnelx.example.com/`.

**Important:** This path-based version shares a single domain and server token. Anyone who knows the public tunnel name can reach that tunnel. Do not use it to expose sensitive admin interfaces, databases, or production credentials. Add per-user/per-tunnel auth, rate limits, quotas, abuse reporting, monitoring, and security review before offering it to other people.

## API

- `GET /healthz` — basic liveness response; no admin authentication.
- `GET /api/status` — active tunnel list; HTTP Basic Auth required.
- `GET /api/logs?limit=100` — recent request logs (max 500); HTTP Basic Auth required.
- `GET /api/metrics` — active tunnel and retained log counts; HTTP Basic Auth required.
- `GET /t/<name>/<path>` — public HTTP request forwarding; no per-tunnel access control in this MVP.
- `GET /_tunnel/agent?name=<name>` — WebSocket agent endpoint; requires `Authorization: Bearer <TUNNELX_TOKEN>`.

## Operational notes and limitations

- Bodies are buffered and capped at 1 MiB; large uploads/downloads and streaming are unsupported.
- Request logs persist as JSONL and include request paths. Avoid query-string secrets in requests. File grows indefinitely; configure log rotation/retention for long-running deployments.
- Logs are in-memory for the last 500 entries plus an append-only JSONL file. There is no search UI or multi-user database yet.
- This implementation forwards ordinary HTTP requests, not raw TCP, UDP, SSH, or WebSocket upgrades to the local application.
- The tunnel uses a shared token. Rotate it on suspected exposure and restart server/agents.
- For public use, keep the server behind HTTPS, do not expose port 8080 publicly, back up logs, and monitor resource use.

## Troubleshooting

- `go mod tidy` cannot resolve host: check internet/DNS and retry.
- Agent gets 401: ensure server and client `TUNNELX_TOKEN` match exactly.
- Tunnel shows offline: confirm client is running and target app is listening on the expected local port.
- HTTPS fails: check DNS, provider firewall, ports 80/443, and Caddy logs.
- Public request returns 502: verify agent connection and local target URL; check server and client logs.

## License

Choose and add a license before publishing or distributing this project.
