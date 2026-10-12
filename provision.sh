#!/usr/bin/env bash
# Idempotent deploy of mybooks to a remote Debian/Ubuntu host.
#
# Reads .env from this directory, builds the binary locally, and over SSH:
#   - creates the `mybooks` system user and /var/lib/mybooks
#   - installs poppler-utils (automatic PDF covers) and Caddy if missing
#   - installs the binary, env file and systemd unit (only when changed)
#   - adds a Caddy site for MYBOOKS_DOMAIN (Let's Encrypt is automatic)
# Running it again with nothing changed restarts nothing.
set -euo pipefail

cd "$(dirname "$0")"
[ -f .env ] || { echo "missing .env (copy .env.example)" >&2; exit 1; }
set -a; . ./.env; set +a

: "${SSH_HOST:?set SSH_HOST in .env}"
: "${MYBOOKS_API_KEY:?set MYBOOKS_API_KEY in .env}"
# Domain defaults to the host part of MYBOOKS_URL.
if [ -z "${MYBOOKS_DOMAIN:-}" ] && [ -n "${MYBOOKS_URL:-}" ]; then
  MYBOOKS_DOMAIN="${MYBOOKS_URL#*://}"; MYBOOKS_DOMAIN="${MYBOOKS_DOMAIN%%/*}"
fi
: "${MYBOOKS_DOMAIN:?set MYBOOKS_DOMAIN or MYBOOKS_URL in .env}"
SSH_USER="${SSH_USER:-root}"
SSH_PORT="${SSH_PORT:-22}"
MYBOOKS_PORT="${MYBOOKS_PORT:-8080}"
MYBOOKS_MAX_UPLOAD_MB="${MYBOOKS_MAX_UPLOAD_MB:-512}"
MYBOOKS_SESSION_DAYS="${MYBOOKS_SESSION_DAYS:-90}"
MYBOOKS_MANAGE_CADDY="${MYBOOKS_MANAGE_CADDY:-1}"

if [ "${#MYBOOKS_API_KEY}" -lt 12 ] || [[ ! "$MYBOOKS_API_KEY" =~ ^[A-Za-z0-9._~+=-]+$ ]]; then
  echo "MYBOOKS_API_KEY must be >= 12 chars of [A-Za-z0-9._~+=-]" >&2
  exit 1
fi

CONTROL_DIR="$(mktemp -d)"
SSH_OPTS=(-p "$SSH_PORT" -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new
  -o ControlMaster=auto -o ControlPath="$CONTROL_DIR/%C" -o ControlPersist=60)
[ -n "${SSH_KEY:-}" ] && SSH_OPTS+=(-i "${SSH_KEY/#\~/$HOME}")
SSH=(ssh)
if [ -n "${SSH_PASS:-}" ]; then
  command -v sshpass >/dev/null || { echo "SSH_PASS requires sshpass (apt install sshpass)" >&2; exit 1; }
  export SSHPASS="$SSH_PASS"   # passed via env, never on the command line
  SSH=(sshpass -e ssh)
  SSH_OPTS+=(-o PreferredAuthentications=password,keyboard-interactive -o PubkeyAuthentication=no)
else
  SSH_OPTS+=(-o BatchMode=yes)
fi
TARGET="$SSH_USER@$SSH_HOST"
BUILD_DIR=""
trap 'ssh -o ControlPath="$CONTROL_DIR/%C" -O exit "$TARGET" 2>/dev/null; rm -rf "$BUILD_DIR" "$CONTROL_DIR"' EXIT
remote() { "${SSH[@]}" "${SSH_OPTS[@]}" "$TARGET" "$@"; }
# Run a script as root on the remote host (sudo if needed).
remote_root() { remote 'if [ "$(id -u)" = 0 ]; then bash -s; else sudo bash -s; fi'; }

log() { printf '\033[1m==> %s\033[0m\n' "$*"; }

# ---- build ----
log "detecting remote architecture"
case "$(remote uname -m)" in
  x86_64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  armv7l|armv6l) GOARCH=arm ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac

VERSION="$(git describe --always --dirty 2>/dev/null || echo dev)"
BUILD_DIR="$(mktemp -d)"
log "building mybooks $VERSION (linux/$GOARCH)"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o "$BUILD_DIR/mybooks" .
LOCAL_SHA="$(sha256sum "$BUILD_DIR/mybooks" | cut -d' ' -f1)"

# ---- binary upload (only when different) ----
REMOTE_SHA="$(remote 'sha256sum /usr/local/bin/mybooks 2>/dev/null | cut -d" " -f1' || true)"
if [ "$LOCAL_SHA" != "$REMOTE_SHA" ]; then
  log "uploading binary"
  remote 'cat > /tmp/mybooks.upload' < "$BUILD_DIR/mybooks"
  UPLOADED=1
else
  log "binary unchanged"
  UPLOADED=0
fi

# ---- config files ----
ENV_FILE="MYBOOKS_API_KEY=$MYBOOKS_API_KEY
MYBOOKS_DATA_DIR=/var/lib/mybooks
MYBOOKS_ADDR=127.0.0.1:$MYBOOKS_PORT
MYBOOKS_MAX_UPLOAD_MB=$MYBOOKS_MAX_UPLOAD_MB
MYBOOKS_SESSION_DAYS=$MYBOOKS_SESSION_DAYS
"

UNIT_FILE="[Unit]
Description=mybooks personal library
After=network-online.target
Wants=network-online.target

[Service]
User=mybooks
Group=mybooks
EnvironmentFile=/etc/mybooks/mybooks.env
ExecStart=/usr/local/bin/mybooks
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/mybooks

[Install]
WantedBy=multi-user.target
"

CADDY_SITE="# managed by mybooks provision.sh
$MYBOOKS_DOMAIN {
	encode gzip
	request_body {
		max_size $((MYBOOKS_MAX_UPLOAD_MB + 64))MB
	}
	reverse_proxy 127.0.0.1:$MYBOOKS_PORT
}
"

b64() { printf '%s' "$1" | base64 -w0; }

log "configuring remote host"
remote_root <<EOF
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
changed=0

# install_file <path> <mode> <owner> <base64 content>; sets changed=1 if different
install_file() {
  local tmp; tmp=\$(mktemp)
  printf '%s' "\$4" | base64 -d > "\$tmp"
  if ! cmp -s "\$tmp" "\$1"; then
    install -m "\$2" -o "\${3%:*}" -g "\${3#*:}" "\$tmp" "\$1"
    echo "  updated \$1"
    rm -f "\$tmp"; return 0
  fi
  rm -f "\$tmp"; return 1
}

id mybooks >/dev/null 2>&1 || { useradd --system --home /var/lib/mybooks --shell /usr/sbin/nologin mybooks; echo "  created user mybooks"; }
install -d -m 750 -o mybooks -g mybooks /var/lib/mybooks
install -d -m 750 -o root -g mybooks /etc/mybooks

if ! command -v pdftoppm >/dev/null || ! command -v curl >/dev/null; then
  echo "  installing poppler-utils curl"
  apt-get update -qq && apt-get install -y -qq poppler-utils curl >/dev/null
fi

if [ "$UPLOADED" = 1 ]; then
  install -m 755 -o root -g root /tmp/mybooks.upload /usr/local/bin/mybooks
  rm -f /tmp/mybooks.upload
  echo "  installed binary"
  changed=1
fi

install_file /etc/mybooks/mybooks.env 640 root:mybooks '$(b64 "$ENV_FILE")' && changed=1
if install_file /etc/systemd/system/mybooks.service 644 root:root '$(b64 "$UNIT_FILE")'; then
  systemctl daemon-reload
  changed=1
fi

systemctl enable mybooks >/dev/null 2>&1
if [ "\$changed" = 1 ] || ! systemctl is-active -q mybooks; then
  systemctl restart mybooks
  echo "  restarted mybooks"
fi

for i in \$(seq 1 20); do
  curl -fsS http://127.0.0.1:$MYBOOKS_PORT/api/health >/dev/null 2>&1 && break
  sleep 0.5
done
curl -fsS http://127.0.0.1:$MYBOOKS_PORT/api/health >/dev/null || { journalctl -u mybooks -n 30 --no-pager; exit 1; }
echo "  mybooks is healthy"

if [ "$MYBOOKS_MANAGE_CADDY" = 1 ]; then
  if ! command -v caddy >/dev/null; then
    echo "  installing caddy"
    apt-get update -qq
    if ! apt-get install -y -qq caddy >/dev/null 2>&1; then
      apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gnupg >/dev/null
      curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
      curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
      apt-get update -qq && apt-get install -y -qq caddy >/dev/null
    fi
  fi

  install -d -m 755 /etc/caddy/conf.d
  touch /etc/caddy/Caddyfile
  grep -qxF 'import /etc/caddy/conf.d/*.caddy' /etc/caddy/Caddyfile || {
    printf '\nimport /etc/caddy/conf.d/*.caddy\n' >> /etc/caddy/Caddyfile
    echo "  added conf.d import to Caddyfile"
    caddy_changed=1
  }

  site=/etc/caddy/conf.d/mybooks.caddy
  [ -f "\$site" ] && cp "\$site" "\$site.bak"
  install_file "\$site" 644 root:root '$(b64 "$CADDY_SITE")' && caddy_changed=1

  if [ "\${caddy_changed:-0}" = 1 ]; then
    if ! caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1; then
      caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile || true
      if [ -f "\$site.bak" ]; then mv "\$site.bak" "\$site"; else rm -f "\$site"; fi
      echo "  caddy config invalid, reverted" >&2
      exit 1
    fi
    systemctl enable caddy >/dev/null 2>&1
    if systemctl is-active -q caddy; then systemctl reload caddy; else systemctl restart caddy; fi
    echo "  reloaded caddy"
  fi
  rm -f "\$site.bak"

  if command -v ufw >/dev/null && ufw status | grep -q 'Status: active'; then
    ufw allow 80/tcp >/dev/null; ufw allow 443/tcp >/dev/null
  fi
fi
EOF

# ---- public check ----
if [ "$MYBOOKS_MANAGE_CADDY" = 1 ]; then
  log "checking https://$MYBOOKS_DOMAIN"
  for i in $(seq 1 10); do
    if curl -fsS "https://$MYBOOKS_DOMAIN/api/health" >/dev/null 2>&1; then
      log "done: https://$MYBOOKS_DOMAIN"
      exit 0
    fi
    sleep 3
  done
  echo "warning: https://$MYBOOKS_DOMAIN not reachable yet (DNS pointing to the server? ports 80/443 open? certificate still being issued?)" >&2
  exit 0
fi
log "done (Caddy not managed: proxy https to 127.0.0.1:$MYBOOKS_PORT yourself)"
