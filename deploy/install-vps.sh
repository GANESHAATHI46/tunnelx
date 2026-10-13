#!/usr/bin/env bash
# ==============================================================================
# TunnelX — Automated 1-Click VPS Installer (Ubuntu / Debian)
# Usage: sudo bash install-vps.sh yourdomain.com
# ==============================================================================
set -euo pipefail

DOMAIN="${1:-}"
if [ -z "$DOMAIN" ]; then
    echo "❌ Error: Please provide your domain name!"
    echo "Usage: sudo bash install-vps.sh yourdomain.com"
    exit 1
fi

echo "🚀 Starting TunnelX installation for domain: $DOMAIN..."

# 1. Update and install dependencies
apt-get update -y
apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl git

# 2. Install Go if not present
if ! command -v go &>/dev/null; then
    echo "📦 Installing Go..."
    curl -fsSL https://go.dev/dl/go1.23.6.linux-amd64.tar.gz -o /tmp/go.tar.gz
    rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tar.gz
    export PATH=$PATH:/usr/local/go/bin
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
fi

# 3. Install Caddy for automatic HTTPS SSL
if ! command -v caddy &>/dev/null; then
    echo "🔒 Installing Caddy (Automatic HTTPS)..."
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -y
    apt-get install -y caddy
fi

# 4. Build TunnelX Server binary
echo "⚙️ Building TunnelX Server binary..."
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$SCRIPT_DIR"
/usr/local/go/bin/go build -trimpath -ldflags="-s -w" -o /usr/local/bin/tunnelx-server ./cmd/server
chmod +x /usr/local/bin/tunnelx-server

# 5. Create user, directories and permissions
id -u tunnelx &>/dev/null || useradd --system --home /opt/tunnelx --shell /usr/sbin/nologin tunnelx
mkdir -p /opt/tunnelx /var/lib/tunnelx /etc/tunnelx
chown -R tunnelx:tunnelx /opt/tunnelx /var/lib/tunnelx

# 6. Generate secure tokens if not exists
ENV_FILE="/etc/tunnelx/tunnelx.env"
if [ ! -f "$ENV_FILE" ]; then
    TOKEN=$(head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 32)
    ADMIN_PASS=$(head -c 16 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 16)
    cat <<EOF > "$ENV_FILE"
TUNNELX_TOKEN=$TOKEN
TUNNELX_ADMIN_USER=admin
TUNNELX_ADMIN_PASS=$ADMIN_PASS
TUNNELX_LISTEN=127.0.0.1:8080
TUNNELX_DATA_DIR=/var/lib/tunnelx
EOF
    chmod 600 "$ENV_FILE"
fi

# Read credentials for output display
source "$ENV_FILE"

# 7. Configure systemd service
cat <<EOF > /etc/systemd/system/tunnelx.service
[Unit]
Description=TunnelX reverse tunnel server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=tunnelx
Group=tunnelx
WorkingDirectory=/opt/tunnelx
EnvironmentFile=/etc/tunnelx/tunnelx.env
Environment=TUNNELX_LISTEN=127.0.0.1:8080
Environment=TUNNELX_DATA_DIR=/var/lib/tunnelx
ExecStart=/usr/local/bin/tunnelx-server
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now tunnelx
systemctl restart tunnelx

# 8. Configure Caddy with Auto-HTTPS
cat <<EOF > /etc/caddy/Caddyfile
$DOMAIN {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8080
    header {
        X-Content-Type-Options nosniff
        Referrer-Policy no-referrer
        X-Frame-Options DENY
    }
}
EOF

systemctl reload caddy

echo "=================================================================="
echo "🎉 TunnelX Server successfully installed and running!"
echo "=================================================================="
echo "Public Domain       : https://$DOMAIN"
echo "Web Dashboard       : https://$DOMAIN/"
echo "Dashboard Username  : $TUNNELX_ADMIN_USER"
echo "Dashboard Password  : $TUNNELX_ADMIN_PASS"
echo "Shared Auth Token   : $TUNNELX_TOKEN"
echo "=================================================================="
echo "To connect from your local PC, run:"
echo "tunnelx set-server wss://$DOMAIN/_tunnel/agent"
echo "tunnelx set-token $TUNNELX_TOKEN"
echo "tunnelx http <port>   (e.g., tunnelx http 3000)"
echo "=================================================================="
