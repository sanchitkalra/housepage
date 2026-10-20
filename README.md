# housepage

A tiny personal playground for publishing static sites and PWAs. Every site claims a name and gets `NAME.<your-domain>`. Coding agents publish through a CLI and a skill. A site can also keep small shared JSON data (a key-value store and collections of documents) that its own page reads and writes.

```
agent / you ──► housepage CLI ──► nginx (app.<domain>/api/) ──► housepaged ──► sites/<name>/ + housepage.db
                                                                    ▲
browser ──► nginx (NAME.<domain>) ── static files from sites/<name>/
                │
                └─ /_api/ ──► housepaged   (the page's own data calls; nginx sets which site it is)
```

Status: v0. Static files, users with tokens, and a per-site data store. No server-side code, no dashboard yet.

## How it works

- **nginx** serves `sites/<name>/` for the host `<name>.<domain>` using one regex `server_name`. Publishing writes files, so nginx never needs a reload. On a site host, `/_api/` goes to housepaged with an `X-Housepage-Site` header that nginx sets from the host name, with per-visitor rate limits.
- **housepaged** (the server) validates uploads, keeps users, tokens, sites and site data in SQLite (`housepage.db`), and writes the files. Schema changes are numbered migrations applied at startup, with a backup before each upgrade.
- **housepage** (the CLI) uploads a folder, manages your tokens and your sites' data settings. Results go to stdout and messages to stderr, so scripts can capture them.
- **`skills/housepage/SKILL.md`** teaches coding agents when and how to use the CLI, including the data store.

## Quick start (local)

```bash
go build -o housepaged ./cmd/housepaged
go build -o housepage  ./cmd/housepage

./housepaged user create --data ./data --superuser me   # prints the user's first token once; only its hash is stored
./housepaged serve --data ./data                        # API on 127.0.0.1:8081

./housepage login --token <token>                       # add --server URL if not local
./housepage publish ./my-site --name my-site            # prints the live URL
./housepage list
./housepage delete my-site
```

`serve` flags: `--data` (holds `sites/` and `housepage.db`), `--addr`, `--domain` (default `localhost:8080`) and `--scheme`. The last two only shape the URLs the API returns.

For nginx locally, run `./run.sh --render-only` with a config file (see below) and use the generated `run/nginx/nginx.conf` as your template: it has the app host, the site host, the `/_api/` location and the rate-limit zones. Start it with `nginx -p run/nginx -c nginx.conf`, then open `http://my-site.<domain>:<port>`. Browsers resolve `*.localhost` to loopback, so a domain like `localhost` needs no DNS.

## Users and tokens

| Who | Can |
|---|---|
| The operator (`housepaged` on the server) | create users, mint any token, list or revoke any token, set a user's site limit |
| A superuser token | create users and change `max_sites` through the API, mint tokens for its own user |
| An ordinary token | publish, list and delete its own sites, mint ordinary tokens for its own user |

```bash
housepaged user create [--data DIR] [--superuser] <name>      # also mints the user's first token, labelled "default"
housepaged user set    [--data DIR] [--max-sites N|default] [--superuser|--no-superuser] <user>
housepaged token create|list|revoke ...                       # operator commands, for any user

housepage token create --label ci [--superuser]               # for yourself; prints it once
housepage token list
housepage token revoke <label>
```

A user may own 10 sites by default (the operator can change it). The routes are in `api-contracts.md`, section 3.

## Site data

A published site can keep data that its own page reads and writes with `fetch('/_api/...')`: no token, same origin. It is **off** until the owner enables it.

```bash
housepage db config my-site --mode open       # visitors read and write; "owner" = visitors read only
housepage db config my-site --max-docs 2000   # flags change only what is given; "default" restores a limit
housepage db show my-site                     # mode, limits, ceilings and usage
```

```js
await fetch('/_api/db/messages', { method: 'POST', body: JSON.stringify({ name, text }) });
const { items, nextCursor } = await (await fetch('/_api/db/messages?order=desc&limit=20')).json();
```

| | |
|---|---|
| Stores | key-value (`/_api/kv/{key}`, any JSON) and collections (`/_api/db/{collection}`, JSON objects with server-made ids) |
| Listing | creation order, `order=desc`, or `sort=<field>` (nested paths work, missing values last); paging by cursor only |
| Limits (default / ceiling) | 5 / 100 collections, 500 / 20,000 documents per collection (and KV entries), 4096 bytes per document |
| Rate limits | about 20 reads and 2 writes per second per visitor, set in nginx |

`open` means anyone with the URL can add, change and delete the data. Every route, error code and example is in `api-contracts.md`; the reasoning is in `site-data_plan.md`.

## Running it for real

`run.sh` installs what is missing (jq, nginx, cloudflared, go), builds the binaries, and starts `housepaged`, nginx and `cloudflared` as background services that the OS restarts if they crash (launchd on macOS, `systemd --user` on Linux). `shutdown.sh` stops and disables them.

```bash
cp housepage.example.json housepage.json   # then edit it; the file is git-ignored because it holds the tunnel token
./run.sh                                   # or ./run.sh other-config.json
./run.sh --render-only                     # generate and check the files under run/ without starting anything
./shutdown.sh
```

| Config key | Meaning | Default |
|---|---|---|
| `domain` | Sites are served at `<name>.<domain>`, the API at `app.<domain>` | required |
| `scheme` | `http` or `https`, used in the URLs the API returns | `https` |
| `dataDir` | Holds `sites/` and `housepage.db` | `~/housepage-data` |
| `nginxPort`, `apiPort` | Local ports (nginx and housepaged listen on loopback only) | 8080, 8081 |
| `tunnelToken` | Token of a Cloudflare named tunnel | required |

The nginx config and the service definitions are generated as plain files in `run/`, and contain the same commands you would type by hand. Cloudflare needs two public hostname rules on the tunnel, added once in its dashboard: `*.<domain>` and `app.<domain>`, both pointing to `http://localhost:<nginxPort>`. Create your user with `./bin/housepaged user create --data <dataDir> --superuser <name>`, then `./bin/housepage login --server https://app.<domain> --token <token>`.

## Rules

- Names: 3-63 lowercase letters, digits and hyphens, no leading or trailing hyphen. Reserved: `app`, `www`, `api`, `admin`, `mail`, `ns1`, `ns2`. Names are first come, first served.
- Limits: 500 files, 5 MB per file, 25 MB total. `index.html` is required at the root. No symlinks.
- Publishing under a name you own replaces the whole site.
- All rules live in `internal/rules` and are enforced by the server. The CLI checks them too, only to fail early.

## Layout

```
cmd/housepage/     CLI: login, publish, list, delete, token, db
cmd/housepaged/    server: serve, migrate status, user, token (operator commands)
  migrations/      numbered SQL files embedded in the binary
internal/rules/    validation, limits and the listing cursor, shared by both
skills/housepage/  agent skill
api-contracts.md   every HTTP route with examples and error codes
site-data_plan.md  design decisions and the build order
migrations_plan.md how migrations work
db-diagram.dbml    the schema (open it at dbdiagram.io)
run.sh             install dependencies, build, start the three services
shutdown.sh        stop the three services
```

## Known limits

- Publishing is wipe-then-write, so a visitor can briefly see a half-written site. Swapping a symlink would make it atomic and give rollback.
- `run.sh` needs a named Cloudflare tunnel. Quick tunnels are not supported.
- Users are created by the operator or by a superuser through the API; the `housepage` CLI has no command for it.
- Request rates are fixed in the nginx template, not per site.
- Listing sorted by a field reads the whole collection each time (about 20 to 30 ms at 20,000 documents); there are no indexes in v0.
- Every site shares the parent domain, so keep any future dashboard on a separate registrable domain.
- Published sites and their data (when enabled) are public.

## Licence

MIT, see `LICENSE`.

## Tests

```bash
go test ./...
```
