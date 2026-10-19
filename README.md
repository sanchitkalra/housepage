# housepage

A tiny personal playground for publishing static sites and PWAs. Every site claims a name and gets `NAME.<your-domain>`. Coding agents publish through a CLI and a skill.

```
agent / you ──► housepage CLI ──PUT multipart──► housepaged ──► sites/<name>/
                                                                    ▲
browser ──► nginx (routes by Host header: NAME.<domain> → sites/NAME/)
```

Status: v0. Static files only, no server-side code, no dashboard yet.

## How it works

- **nginx** serves `sites/<name>/` for the host `<name>.<domain>` using one regex `server_name`. Publishing writes files, so nginx never needs a reload.
- **housepaged** (the server) validates uploads, tracks which token owns which name in SQLite, and writes the files.
- **housepage** (the CLI) uploads a folder. It prints the live URL on stdout, so agents and scripts can read it.
- **`skills/housepage/SKILL.md`** teaches coding agents when and how to use the CLI.

## Quick start (local)

```bash
go build -o housepaged ./cmd/housepaged
go build -o housepage  ./cmd/housepage

./housepaged token create --data ./data me          # prints a token once; only its hash is stored
./housepaged serve --data ./data                    # API on 127.0.0.1:8081

./housepage login --token <token>                   # add --server URL if not local
./housepage publish ./my-site --name my-site        # prints the live URL
./housepage list
./housepage delete my-site
```

`serve` flags: `--data` (holds `sites/` and `housepage.db`), `--addr`, `--domain` (default `localhost:8080`) and `--scheme`. The last two only shape the URLs the API returns.

Point nginx at the same data directory. A minimal local config:

```nginx
http {
    include mime.types;                       # add: application/manifest+json webmanifest;
    limit_req_zone $binary_remote_addr zone=api:10m rate=30r/m;
    limit_req_status 429;

    server { listen 8080 default_server; return 404 "no such site\n"; }

    server {                                  # the API; an exact name beats the regex below
        listen 8080;
        server_name app.localhost;
        client_max_body_size 30m;             # nginx's default of 1m would reject uploads
        location /api/ {
            limit_req zone=api burst=10 nodelay;
            proxy_pass http://127.0.0.1:8081;
            proxy_set_header Host $host;
        }
        location / { return 404; }
    }

    server {                                  # the sites
        listen 8080;
        server_name ~^(?<page>[a-z0-9-]+)\.localhost$;
        root sites/$page;                     # relative to nginx's -p prefix (the data dir)
        index index.html;
        add_header Cache-Control "no-cache" always;
    }
}
```

Run it with `nginx -p <data-dir> -c nginx.conf`, then open `http://my-site.localhost:8080`. Browsers resolve `*.localhost` to loopback, so no DNS is needed.

## Rules

- Names: 3-63 lowercase letters, digits and hyphens, no leading or trailing hyphen. Reserved: `app`, `www`, `api`, `admin`, `mail`, `ns1`, `ns2`. Names are first come, first served.
- Limits: 500 files, 5 MB per file, 25 MB total. `index.html` is required at the root. No symlinks.
- Publishing under a name you own replaces the whole site.
- All rules live in `internal/rules` and are enforced by the server. The CLI checks them too, only to fail early.

## Layout

```
cmd/housepage/     CLI: login, publish, list, delete
cmd/housepaged/    server: serve, token create
internal/rules/    validation shared by both
skills/housepage/  agent skill
```

## Known limits

- Publishing is wipe-then-write, so a visitor can briefly see a half-written site. Swapping a symlink would make it atomic and give rollback.
- Tokens are created by hand with `housepaged token create`. There are no accounts, quotas or per-token rate limits.
- Behind a Cloudflare tunnel, per-IP rate limiting needs nginx's `real_ip` module with `CF-Connecting-IP`. Without it every request looks like one client.
- Every site shares the parent domain, so keep any future dashboard on a separate registrable domain.
- Published sites are public.

## Tests

```bash
go test ./...
```
