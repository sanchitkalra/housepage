#!/usr/bin/env bash
# Installs missing dependencies, builds housepage, then starts housepaged, nginx and cloudflared as
# background services that the OS restarts if they crash (launchd on macOS, systemd --user on Linux).
# Safe to re-run: it applies config changes. Stop everything with ./shutdown.sh.
#   ./run.sh [config.json] [--render-only]      (default config: ./housepage.json)
# --render-only writes and checks the generated files under run/ and changes nothing else.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
RUN=$ROOT/run
CONFIG="" RENDER_ONLY=0
PLACEHOLDER="PASTE_YOUR_TUNNEL_TOKEN_HERE"
SERVICES="housepaged nginx cloudflared"

die() { echo "error: $*" >&2; exit 1; }
log() { echo "==> $*"; }

for arg in "$@"; do
	case "$arg" in
	--render-only) RENDER_ONLY=1 ;;
	-h | --help) sed -n '2,6p' "$0"; exit 0 ;;
	*) CONFIG=$arg ;;
	esac
done
CONFIG=${CONFIG:-$ROOT/housepage.json}
[ -f "$CONFIG" ] || die "config not found: $CONFIG (copy housepage.example.json to housepage.json and edit it)"

case "$(uname -s)" in
Darwin) OS=mac ;;
Linux) OS=linux ;;
*) die "unsupported OS: $(uname -s)" ;;
esac
SUDO=""; [ "$(id -u)" = 0 ] || SUDO=sudo

# ---------- dependencies ----------
INSTALLED_NGINX=0
need() { # need <tool> <brew formula> <apt package> ; apt-less tools are handled by the caller
	command -v "$1" >/dev/null && return 0
	[ "$RENDER_ONLY" = 0 ] || die "$1 is not installed (--render-only does not install anything)"
	log "installing $1"
	if [ "$OS" = mac ]; then
		command -v brew >/dev/null || die "Homebrew is required to install $1: https://brew.sh"
		brew install "$2"
	else
		command -v apt-get >/dev/null || die "install $1 manually; this script only knows apt-get"
		$SUDO apt-get install -y "$3"
	fi
	[ "$1" != nginx ] || INSTALLED_NGINX=1
}

go_ok() { command -v go >/dev/null && [ "$(go env GOVERSION | sed 's/^go1\.\([0-9]*\).*/\1/')" -ge 21 ]; }

install_go_linux() {
	local ver arch
	ver=$(awk '/^go /{print $2}' "$ROOT/go.mod")
	case "$(uname -m)" in aarch64) arch=arm64 ;; x86_64) arch=amd64 ;; armv7l) arch=armv6l ;; *) die "no Go download for $(uname -m)" ;; esac
	log "installing Go $ver to ~/.local/go"
	mkdir -p "$HOME/.local"
	curl -fsSL "https://go.dev/dl/go$ver.linux-$arch.tar.gz" | tar -C "$HOME/.local" -xz
	export PATH="$HOME/.local/go/bin:$PATH"
}

install_cloudflared_linux() {
	local arch
	case "$(uname -m)" in aarch64) arch=arm64 ;; x86_64) arch=amd64 ;; armv7l) arch=arm ;; *) die "no cloudflared download for $(uname -m)" ;; esac
	log "installing cloudflared to ~/.local/bin"
	mkdir -p "$HOME/.local/bin"
	curl -fsSL -o "$HOME/.local/bin/cloudflared" "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-$arch"
	chmod +x "$HOME/.local/bin/cloudflared"
	export PATH="$HOME/.local/bin:$PATH"
}

need jq jq jq
need nginx nginx nginx
if [ "$INSTALLED_NGINX" = 1 ] && [ "$OS" = linux ]; then
	$SUDO systemctl disable --now nginx || true # the package starts a system nginx on port 80; we run our own
fi
if ! command -v cloudflared >/dev/null; then
	[ "$RENDER_ONLY" = 0 ] || die "cloudflared is not installed (--render-only does not install anything)"
	if [ "$OS" = mac ]; then need cloudflared cloudflared cloudflared; else install_cloudflared_linux; fi
fi
if ! go_ok; then
	[ "$RENDER_ONLY" = 0 ] || die "go 1.21 or newer is not installed (--render-only does not install anything)"
	if [ "$OS" = mac ]; then brew install go; else install_go_linux; fi
fi

# ---------- config ----------
cfg() { jq -r ".$1 // empty" "$CONFIG"; }
DOMAIN=$(cfg domain)
SCHEME=$(cfg scheme); SCHEME=${SCHEME:-https}
DATA=$(cfg dataDir); DATA=${DATA:-~/housepage-data}; DATA=${DATA/#\~/$HOME}
NGINX_PORT=$(cfg nginxPort); NGINX_PORT=${NGINX_PORT:-8080}
API_PORT=$(cfg apiPort); API_PORT=${API_PORT:-8081}
TOKEN=$(cfg tunnelToken)

[[ $DOMAIN =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ && $DOMAIN == *.* ]] || die "config: domain must look like example.dev (lowercase, no scheme or path)"
[[ $SCHEME == http || $SCHEME == https ]] || die "config: scheme must be http or https"
[[ $NGINX_PORT =~ ^[0-9]+$ && $API_PORT =~ ^[0-9]+$ ]] || die "config: nginxPort and apiPort must be numbers"
[ "$NGINX_PORT" != "$API_PORT" ] || die "config: nginxPort and apiPort must differ"
{ [ -n "$TOKEN" ] && [ "$TOKEN" != "$PLACEHOLDER" ]; } || die "config: tunnelToken is required (Cloudflare Zero Trust > Networks > Tunnels > create a tunnel > copy its token)"
DOMAIN_RE=${DOMAIN//./\\.}

# ---------- build ----------
if [ "$RENDER_ONLY" = 0 ]; then
	log "building"
	mkdir -p "$ROOT/bin"
	(cd "$ROOT" && go build -o bin/housepaged ./cmd/housepaged && go build -o bin/housepage ./cmd/housepage)
fi

# ---------- nginx config ----------
mkdir -p "$RUN/nginx" "$RUN/logs" "$RUN/services"
MIME=""
for p in "$(command -v brew >/dev/null && echo "$(brew --prefix)/etc/nginx/mime.types")" /etc/nginx/mime.types /usr/local/etc/nginx/mime.types; do
	[ -n "$p" ] && [ -f "$p" ] && { MIME=$p; break; }
done
[ -n "$MIME" ] || die "could not find nginx's mime.types"
cp "$MIME" "$RUN/nginx/mime.types"
chmod u+w "$RUN/nginx/mime.types"
grep -q webmanifest "$RUN/nginx/mime.types" || perl -pi -e 's|^types \{|types {\n    application/manifest+json webmanifest;|' "$RUN/nginx/mime.types"

NGINX_CONF=$(cat <<'EOF'
# generated by run.sh from the config file; edit the config, not this file
daemon off;
worker_processes 1;
pid @RUN@/nginx/nginx.pid;
error_log @RUN@/logs/nginx-error.log info;
events { worker_connections 128; }
http {
    include mime.types;
    log_format hosts '$remote_addr $host "$request" $status';
    access_log @RUN@/logs/nginx-access.log hosts;
    limit_req_zone $binary_remote_addr zone=api:10m rate=30r/m;
    limit_req_status 429;

    # anything that matches nothing else
    server {
        listen 127.0.0.1:@NGINX_PORT@ default_server;
        return 404 "no such site\n";
    }

    # the API: app.<domain>
    server {
        listen 127.0.0.1:@NGINX_PORT@;
        server_name app.@DOMAIN@;
        client_max_body_size 30m;
        set_real_ip_from 127.0.0.1;
        real_ip_header CF-Connecting-IP;
        location /api/ {
            limit_req zone=api burst=10 nodelay;
            proxy_pass http://127.0.0.1:@API_PORT@;
            proxy_set_header Host $host;
            proxy_read_timeout 120s;
        }
        location / { return 404 "no such page\n"; }
    }

    # the sites: <name>.<domain>  ->  <dataDir>/sites/<name>/
    server {
        listen 127.0.0.1:@NGINX_PORT@;
        server_name ~^(?<page>[a-z0-9-]+)\.@DOMAIN_RE@$;
        root @DATA@/sites/$page;
        index index.html;
        add_header Cache-Control "no-cache" always;
    }
}
EOF
)
NGINX_CONF=${NGINX_CONF//@RUN@/$RUN}
NGINX_CONF=${NGINX_CONF//@NGINX_PORT@/$NGINX_PORT}
NGINX_CONF=${NGINX_CONF//@API_PORT@/$API_PORT}
NGINX_CONF=${NGINX_CONF//@DOMAIN_RE@/$DOMAIN_RE}
NGINX_CONF=${NGINX_CONF//@DOMAIN@/$DOMAIN}
NGINX_CONF=${NGINX_CONF//@DATA@/$DATA}
printf '%s\n' "$NGINX_CONF" >"$RUN/nginx/nginx.conf"
NGINX_BIN=$(command -v nginx)
CF_BIN=$(command -v cloudflared)
"$NGINX_BIN" -t -p "$RUN/nginx" -c nginx.conf || die "the generated nginx config is invalid: $RUN/nginx/nginx.conf"

# ---------- service definitions (plain files you can read and copy) ----------
svc_cmd() { # prints the command line for a service, one NUL-terminated argument at a time
	case $1 in
	housepaged) printf '%s\0' "$ROOT/bin/housepaged" serve --data "$DATA" --addr "127.0.0.1:$API_PORT" --domain "$DOMAIN" --scheme "$SCHEME" ;;
	nginx) printf '%s\0' "$NGINX_BIN" -p "$RUN/nginx" -c nginx.conf ;;
	cloudflared) printf '%s\0' "$CF_BIN" tunnel --no-autoupdate run ;; # the token is passed as TUNNEL_TOKEN
	esac
}
xml() { local s=$1; s=${s//&/&amp;}; s=${s//</&lt;}; s=${s//>/&gt;}; printf '%s' "$s"; }

render_service() { # render_service <name>
	local n=$1 a out
	local args=()
	while IFS= read -r -d '' a; do args+=("$a"); done < <(svc_cmd "$n")
	if [ "$OS" = mac ]; then
		out=$RUN/services/dev.housepage.$n.plist
		{
			echo '<?xml version="1.0" encoding="UTF-8"?>'
			echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
			echo '<plist version="1.0"><dict>'
			echo "  <key>Label</key><string>dev.housepage.$n</string>"
			echo '  <key>ProgramArguments</key><array>'
			for a in "${args[@]}"; do echo "    <string>$(xml "$a")</string>"; done
			echo '  </array>'
			echo "  <key>WorkingDirectory</key><string>$(xml "$RUN")</string>"
			[ "$n" != cloudflared ] || echo "  <key>EnvironmentVariables</key><dict><key>TUNNEL_TOKEN</key><string>$(xml "$TOKEN")</string></dict>"
			echo '  <key>KeepAlive</key><true/>'
			echo '  <key>RunAtLoad</key><true/>'
			echo '  <key>ThrottleInterval</key><integer>5</integer>'
			echo "  <key>StandardOutPath</key><string>$(xml "$RUN")/logs/$n.log</string>"
			echo "  <key>StandardErrorPath</key><string>$(xml "$RUN")/logs/$n.log</string>"
			echo '</dict></plist>'
		} >"$out"
	else
		out=$RUN/services/housepage-$n.service
		{
			echo '[Unit]'
			echo "Description=housepage $n"
			echo 'After=network-online.target'
			echo
			echo '[Service]'
			printf 'ExecStart='; printf '"%s" ' "${args[@]}"; echo
			echo "WorkingDirectory=$RUN"
			[ "$n" != cloudflared ] || echo "Environment=\"TUNNEL_TOKEN=$TOKEN\""
			echo 'Restart=on-failure'
			echo 'RestartSec=3'
			echo "StandardOutput=append:$RUN/logs/$n.log"
			echo "StandardError=append:$RUN/logs/$n.log"
			echo
			echo '[Install]'
			echo 'WantedBy=default.target'
		} >"$out"
	fi
	chmod 600 "$out"
}
for n in $SERVICES; do render_service "$n"; done
log "rendered $RUN/nginx/nginx.conf and the service definitions in $RUN/services/ (nginx -t passed)"
[ "$RENDER_ONLY" = 0 ] || exit 0

# ---------- (re)start ----------
mkdir -p "$DATA"
"$ROOT/shutdown.sh" >/dev/null 2>&1 || true # stop our own services first, so a re-run applies new config
for port in $NGINX_PORT $API_PORT; do
	if command -v lsof >/dev/null && lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
		die "port $port is already in use by another program (stop it, or change the port in $CONFIG)"
	fi
done
: >"$RUN/logs/cloudflared.log"

if [ "$OS" = mac ]; then
	mkdir -p "$HOME/Library/LaunchAgents"
	for n in $SERVICES; do
		label=dev.housepage.$n
		cp "$RUN/services/$label.plist" "$HOME/Library/LaunchAgents/$label.plist"
		chmod 600 "$HOME/Library/LaunchAgents/$label.plist"
		launchctl enable "gui/$UID/$label"
		launchctl bootstrap "gui/$UID" "$HOME/Library/LaunchAgents/$label.plist"
	done
else
	mkdir -p "$HOME/.config/systemd/user"
	for n in $SERVICES; do
		cp "$RUN/services/housepage-$n.service" "$HOME/.config/systemd/user/housepage-$n.service"
		chmod 600 "$HOME/.config/systemd/user/housepage-$n.service"
	done
	systemctl --user daemon-reload
	for n in $SERVICES; do systemctl --user enable --now "housepage-$n.service"; done
	loginctl enable-linger "$USER" 2>/dev/null || log "to keep the services running after logout and start them at boot, run: sudo loginctl enable-linger $USER"
fi

# ---------- check ----------
code=""
for _ in $(seq 15); do
	code=$(curl -s -o /dev/null -w '%{http_code}' -H "Host: app.$DOMAIN" "http://127.0.0.1:$NGINX_PORT/api/v1/sites" || true)
	[ "$code" = 401 ] && break
	sleep 1
done
if [ "$code" = 401 ]; then log "ok: nginx reaches housepaged (401 without a token, as expected)"; else log "WARNING: nginx -> housepaged check returned '$code'; see $RUN/logs/"; fi
connected=0
for _ in $(seq 20); do
	grep -q "Registered tunnel connection" "$RUN/logs/cloudflared.log" 2>/dev/null && { connected=1; break; }
	sleep 1
done
if [ "$connected" = 1 ]; then log "ok: the tunnel is connected"; else log "WARNING: the tunnel has not connected yet; see $RUN/logs/cloudflared.log"; fi

cat <<EOF

Running. Logs are in $RUN/logs/. Stop everything with ./shutdown.sh.

Cloudflare needs two public hostname rules on this tunnel (added once, in the dashboard):
  *.$DOMAIN   ->  HTTP  localhost:$NGINX_PORT
  app.$DOMAIN ->  HTTP  localhost:$NGINX_PORT

Create a token, then log in from the machine you publish from:
  $ROOT/bin/housepaged token create --data $DATA <name>
  $ROOT/bin/housepage login --server $SCHEME://app.$DOMAIN --token <token>
EOF
