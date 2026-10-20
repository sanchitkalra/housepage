# housepage

A personal playground for publishing static sites and PWAs. A site claims a name and is served at `NAME.<domain>`. Coding agents publish with a CLI that a skill teaches them to use. A site can keep small shared JSON data (key-value and collections) that its own page reads and writes. Inspired by Shopify's "quick" (shopify.engineering/quick). A standalone project.

> **Resuming work? Read `HANDOFF.md` first.** It records the working agreement with the user and how the build went (all 15 modules of `site-data_plan.md` are done).

## How to work with this user

- **Do not make design decisions or unrequested changes.** Propose, get a yes, then build. This went wrong once already: an installer, Go changes and a config redesign were made unasked and had to be reverted. When a request is ambiguous, restate your understanding and confirm before coding.
- **Never commit or push unless asked.** "Commit" does not mean "push".
- Keep it simple: v0 first. Mark deliberate shortcuts with a `// ponytail:` comment naming the ceiling and the upgrade path.
- The user knows Java and is new to Go. Explain Go idioms briefly when you introduce them. A private reading guide exists as an artifact ("Housepage Code Tour": https://claude.ai/artifact/Pcy77251F571bh5nwTNjiy). It mirrors the Go code as of commit `4f7f220` and goes stale if the code changes.
- Prefer tables and diagrams over paragraphs when explaining.

## Architecture

```
agent / user ──► housepage CLI ──► nginx (app.<domain>/api/) ──► housepaged ──► <dataDir>/sites/<name>/ + housepage.db
                                                                                         ▲
browser ──► Cloudflare tunnel ──► nginx (name.<domain>, by Host header) ── static files ─┘
                                        └─ /_api/ (header X-Housepage-Site set by nginx) ──► housepaged
```

| Piece | Role |
|---|---|
| `cmd/housepaged` | Server. `serve`; `migrate status`; operator commands `user create/set`, `token create/list/revoke`. SQLite with embedded numbered migrations (`migrations/`), applied at startup with a backup first. One connection in the pool: inside a transaction use only the `tx` |
| `cmd/housepage` | Client CLI: `login`, `publish`, `list`, `delete`, `token create/list/revoke`, `db config/show`. Results to stdout, messages to stderr. Config in `os.UserConfigDir()/housepage/config.json` (mode 0600) |
| `internal/rules` | Validation, limits and the listing cursor shared by both. The server **must** enforce them; the client only uses them to fail early |
| `skills/housepage/SKILL.md` | Teaches coding agents to publish and to use the data store, and to ask the user before enabling `open` mode |
| nginx | Not Go. Serves site files by Host header, proxies `/api/` (30 requests a minute) and the sites' `/_api/` (20 reads and 2 writes a second per visitor, 16 KB bodies), sets `X-Housepage-Site` |
| cloudflared | Tunnel to the internet. Named tunnel with a token; **no quick tunnels** |

Where to look: `api-contracts.md` (every route, with examples and error codes), `site-data_plan.md` (design decisions and build order), `migrations_plan.md`, `db-diagram.dbml`.

API (all but the site-host data routes need `Authorization: Bearer hp_…`): publishing `PUT/GET/DELETE /api/v1/sites[/{name}]` (plain-text errors), users and tokens under `/api/v1/users` and `/api/v1/tokens`, site settings `GET/PUT /api/v1/sites/{site}/db-config`, and the data routes (`kv/…`, `db/…`) under `/api/v1/sites/{site}/` for the owner or `/_api/` on the site host (no token, the site's data mode decides). New routes answer JSON errors `{error, message}`.

## Rules

- Name: 3-63 chars, `[a-z0-9-]`, no leading or trailing hyphen, no `xn--` prefix. Reserved: `app www api admin mail ns1 ns2`. Names are flat and first come, first served.
- Limits: 500 files, 5 MB per file, 25 MB total, `index.html` required at the root, no symlinks. A publish replaces the whole site.
- Order in `putSite`: authenticate, read the body, validate, claim the name, write. Validation comes before the claim so a rejected upload never takes a name.

## Commands

```bash
go test ./...                                    # all tests (standard library, plus modernc.org/sqlite as the only direct dependency)
go build -o bin/housepaged ./cmd/housepaged
go build -o bin/housepage  ./cmd/housepage
./bin/housepaged user create --data DIR --superuser me   # prints the user's first token once
./bin/housepaged serve --data DIR --domain example.dev --scheme https
./run.sh [config.json] [--render-only]           # installs deps, builds, starts the 3 services; --render-only changes nothing outside run/
./shutdown.sh                                    # stops and disables the 3 services
```

`housepage.json` (git-ignored, holds the tunnel token): `domain`, `scheme`, `dataDir`, `nginxPort` (8080), `apiPort` (8081), `tunnelToken` (**required**, `run.sh` errors without it). Copy `housepage.example.json`. `--domain` and `--scheme` on `housepaged` only shape the URLs it returns; routing is nginx's `server_name`, which `run.sh` templates from the same config. Do not make nginx or the server domain-agnostic without asking: that was tried and the user chose templating instead.

## Status

| Done | Notes |
|---|---|
| Rules, server, CLI, skill | Tests pass |
| Users, tokens, site limit, migrations | Operator commands, API and client commands |
| Site data store | Writes, listing with cursors, settings and usage, both mounts, nginx limits. Verified end to end through real nginx, and with headless agents using the skill |
| `run.sh`, `shutdown.sh` | `--render-only` verified (config checks, `nginx -t`, plists lint clean). **The real start, supervision and shutdown cycle, the build step and the whole Linux path are untested** |

| Next (nothing here is started) |
|---|
| Buy `housepage.dev` (chosen and seen as available), put its DNS on Cloudflare |
| Create a named tunnel, copy its token, add two public hostname rules by hand in the dashboard: `*.<domain>` and `app.<domain>`, both to `http://localhost:<nginxPort>` |
| First real `./run.sh`, then test crash-restart and `./shutdown.sh`. Note: port 8080 may be held by the old playground nginx (see below) |
| Raspberry Pi hosting (Linux path of `run.sh`) |
| Skill and sample page: add `<link rel="apple-touch-icon">` and a favicon (iOS probes `/apple-touch-icon.png` and 404s) |
| nginx access log has no timestamps; add `$time_local` to `log_format` |
| Choose a licence (the repo is public and has none) |

Ideas the user has not asked for yet, so do not build unprompted: atomic publish via symlink swap (v0 is wipe-then-write), a dashboard on a separate registrable domain, zip upload, and the data store's future work listed in `site-data_plan.md` (`increment` and `merge` updates, indexes, per-collection access rules, private reads, realtime, per-site rate limits).

## Gotchas learned

- Go's `multipart.Part.FileName()` strips directories, so `css/app.css` would become `app.css`. `readFiles` parses `Content-Disposition` itself.
- nginx `$host` drops the port, so a local setup with a port needs `$http_host`. `30m` means 30 MiB (31,457,280 bytes). The default `client_max_body_size` of 1m rejects uploads.
- Behind the tunnel every request comes from `127.0.0.1`. `set_real_ip_from 127.0.0.1` plus `real_ip_header CF-Connecting-IP` makes rate limits per visitor. Verified: Cloudflare passes `Host` unchanged, `X-Forwarded-Proto: https` and `CF-Connecting-IP`.
- A quick tunnel gives one random hostname and no wildcards. `cloudflared tunnel --url … --http-host-header <name>.localhost` makes it present as a specific site. That was only a testing trick.
- Chromium on macOS installs PWAs as a broken, unsigned app shim ("damaged"). Test PWA installs in Safari (Add to Dock) or on a phone.
- macOS ships bash 3.2: no namerefs or associative arrays in the scripts.
- Safety: all hostnames and paths are constrained to `[a-z0-9-]` and relative segments before they reach the filesystem, in the nginx regex and in `rules`. Keep both.
- A cookie or origin concern remains: sites are siblings on one domain. A future dashboard should live on a different registrable domain.

## Outside the repo

`~/playground/` holds the nginx config and sites used while learning (nginx on port 8080 with `-p ~/playground -c nginx.conf`, SQLite DB, a few test sites). It is not part of the project, and may still be running.
