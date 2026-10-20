# Site data API: plan

Gives a published site shared state: a key-value store and collections of JSON documents, called from the page's own JavaScript. Inspired by Quick's `quick.db`. Everything here was decided in conversation; nothing is built yet. The schema is in `db-diagram.dbml` (open it at dbdiagram.io).

## Decisions

| Area | Decision |
|---|---|
| Storage | One table `docs(site, collection, id, value JSON, created_at, updated_at)`, primary key `(site, collection, id)`, `WITHOUT ROWID`. KV is the reserved collection `_kv`. No stored counts or sizes: everything is derived. |
| Ids | Collections: server-generated ULIDs (id order is creation order). KV: caller-chosen keys, validated. Collections never accept caller-chosen ids. |
| Documents | Envelope `{id, createdAt, updatedAt, data}`. A document is a JSON object, a KV value is any JSON. |
| Listing | Full documents, no keys-only mode. Default order is creation order. `sort=<field>` sorts by a JSON field (nested paths allowed). Missing values sort last in either direction. Text keys compare on their first 256 characters. |
| Pagination | Cursor based, nothing else. First request: `sort`, `order`, `limit`. Later requests: `cursor` only (and optionally `limit`). Sort params sent with a cursor must match it, else 400. No `offset`, no page numbers: the API docs must say so. |
| Updates | `PUT ...?strategy=`. Only `replace` exists in v0 and it is the default. An unknown strategy is a 400 that lists the valid ones. `increment` and `merge` are later. |
| Modes | Per site: `off` (default), `open` (anyone reads and writes), `owner` (anyone reads, only the owner's token writes). Reads are public whenever the mode is not `off`. |
| Limits | Defaults: 5 collections, 500 documents per collection, 4 KB per document, `_kv` has its own 500-entry cap. Ceilings (server-wide): 100, 20,000, 4 KB (about 8 GB worst case per site). Owners may set any value up to a ceiling. |
| Counting | Checked on write. A collection exists while it has documents, so emptying one frees its slot. At a cap: new documents and collections are refused, replace/delete/read still work. Lowering a cap below usage blocks only new inserts. |
| Errors | The data API returns JSON `{error, message}`. 400 invalid, 401 token, 403 mode, 404 missing, 409 count cap, 413 document too large, 429 rate limit. The existing publish API keeps plain-text errors. |
| Indexes | None in v0. |
| Users | A real `users` table: `id` is the identity, `name` is a display property (kept unique so operator commands can address a user by name), `is_superuser` flag, `max_sites`. `sites.owner_id` points at the user who owns the site. "Owner" elsewhere in this plan means that user. |
| Tokens | A user can hold any number of tokens, each with a required `label`, unique per user. A token has its own `is_superuser` flag, which may be 1 only if its user is a superuser (a trigger rejects anything else, and demoting a user downgrades their superuser tokens). So a superuser can keep an ordinary token for everyday publishing and use a superuser token only for administration. |
| Site cap | Default 10 sites per user, stored as `users.max_sites` (NULL = default). No ceiling, because only operator-level actors can change it. Checked when a new name is claimed; at the cap the publish is refused with 403 `site limit reached: 10 of 10` (plain text, like the rest of the publish API). Republishing a site the user already owns always works, and deleting a site frees a slot. |
| Who changes `max_sites` | Only the `housepaged` CLI on the server (`housepaged user set --max-sites N <user>`) or `PUT /api/v1/users/{name}` with a **superuser token**. Users cannot change their own, and ordinary tokens get 403. |
| Minting | Only superusers mint superuser tokens. Full rules in the section below. |
| Migrations | Numbered `.sql` files embedded in the binary, applied in order at startup. `0001_init.sql` is the full baseline schema, with no data migration since nothing is deployed yet. |

## Users, tokens, migrations and the site cap

Who may create which user or token:

| Actor | May do |
|---|---|
| The server operator (`housepaged` CLI) | Everything: create users, mint any token for any user, set `max_sites`. The only way to bootstrap the first superuser. |
| Superuser token (API or `housepage` CLI) | Create users, including other superusers (API only). Mint ordinary or superuser tokens, **only for its own user**. Change `max_sites`. |
| Ordinary token (API or `housepage` CLI) | Mint ordinary tokens, **only for its own user**. Nothing else. |
| `housepage` client CLI | Mints tokens for the logged-in user. It cannot create users. |

Rules that follow: nobody mints a token for another user except the operator, and the default token created together with a new user (below). Minting a superuser token needs a *superuser token*, not just a superuser account.

- **Creating a user always mints that user's first token, labelled `default`, in the same step**, so a new account is never unusable. This holds for `housepaged user create` and for `POST /api/v1/users`. The plaintext is shown once. The default token matches the account: a superuser account gets a superuser `default` token, an ordinary account gets an ordinary one. A new superuser can then mint an ordinary token for everyday use.
- **Operator commands:** `housepaged user create <name> [--superuser]` (prints the default token), `housepaged user set --max-sites N <user>`, `housepaged token create <user> --label <label> [--superuser]` for further tokens, `housepaged token list [<user>]` and `housepaged token revoke <user> <label>` (the operator can list and revoke any user's tokens). `token create` fails if the user does not exist, so a typo never creates an account.
- **API routes (proposal):** `POST /api/v1/users` (superuser token only; returns the user and its default token once), `GET` and `PUT /api/v1/users/{name}`, and `POST /api/v1/tokens` with `{label, superuser?}`, always for the caller's own user (the plaintext token is returned once), `GET /api/v1/tokens` (the caller's own tokens, never the hashes) and `DELETE /api/v1/tokens/{label}` (revoke one of the caller's own tokens).
- **Enforcement:** two triggers in `0001_init.sql` (a token with `is_superuser = 1` for a non-superuser is rejected on insert and update; demoting a user clears the flag on their tokens). The API also checks the caller's rights first, so the client sees a clean 403 and the triggers are the backstop.

- **Migrations are numbered `.sql` files** (`cmd/housepaged/migrations/0001_init.sql`, `0002_….sql`, and so on), embedded in the binary and applied in order at startup. The applied version is tracked with `PRAGMA user_version`. Each file runs in one transaction, so a failing migration rolls back and startup stops.
- **`0001_init.sql` is the baseline and creates the whole schema in the diagram.** Nothing is deployed yet, so there is no data to migrate: no backfill, no table rebuilds. Until the first real deploy, `0001` may be edited in place. After that, files are append-only and a change means a new file.
- **Old development databases are not upgraded.** One created by the earlier code (tables `tokens` and `sites` with a text `owner` column, version 0) is detected at startup and refused with a clear message to delete it, instead of failing with "table already exists".
- **Foreign keys are switched on** for every connection (`_pragma=foreign_keys(1)`), otherwise the cascade from `sites` to `docs` and `site_db_config` does nothing.
- **Claiming a name** counts the user's sites and inserts in one transaction, so two simultaneous publishes cannot both slip under the cap.

## Cursor

`base64url(JSON)` without padding: `{s: sort path, o: order, n: 0|1 (no value), v: last sort value, i: last id}`. Values are bound as parameters, never concatenated. Default order cursor is just the last id (`WHERE id > ?`). Fetch `limit + 1` rows to decide whether `nextCursor` is null. Sorted queries:

```
n=0:  f IS NOT NULL AND (f > :v OR (f = :v AND id > :id))  OR  f IS NULL      -- flip > to < for desc
n=1:  f IS NULL AND id > :id                                                   -- f = json_extract(value, ?)
```

## Routes

| | Site host (pages) | App host (owner, bearer token) |
|---|---|---|
| Prefix | `/_api/` | `/api/v1/sites/<site>/` |
| Auth | none, the mode decides | owner only, any mode |

| Route (after the prefix) | Result |
|---|---|
| `GET kv/{key}` | 200 envelope, or 404 |
| `PUT kv/{key}` | any JSON body. 201 created, 200 replaced |
| `DELETE kv/{key}` | 204, also when absent |
| `GET kv?prefix=&limit=&cursor=` | `{items, nextCursor}` in key order |
| `POST db/{collection}` | 201 with the document and its new id |
| `GET db/{collection}/{id}` | 200 or 404 |
| `PUT db/{collection}/{id}?strategy=` | 404 if the id does not exist (no upsert) |
| `DELETE db/{collection}/{id}` | 204 |
| `GET db/{collection}?sort=&order=&limit=` / `?cursor=` | `{items, nextCursor}` |
| `GET` / `PUT db-config` (app host only) | read and set mode and limits |

Handlers are shared by both entry points. nginx's site block gets `location /_api/` that proxies to housepaged and sets `X-Housepage-Site $page`, overwriting anything the client sent. housepaged listens on loopback only, and `/_api/` is not proxied on the app host, so the header cannot be forged from outside. A site cannot serve its own file at `/_api/`.

## CLI

| Command | Does |
|---|---|
| `housepage db config <site> --mode off\|open\|owner --max-collections N --max-docs N --max-doc-size N` | sets only the flags given. Values above a ceiling are refused, naming the ceiling |
| `housepage db show <site>` | mode, effective limits, ceilings and current usage |
| `housepage token create --label <label> [--superuser]` | mints a token for the logged-in user and prints it once. `--superuser` needs a superuser token. |
| `housepage token list` | the user's own tokens: label, level (ordinary or superuser), created date. Never the hash or the plaintext. |
| `housepage token revoke <label>` | deletes one of the user's own tokens. It stops working immediately. |

Owner-mode writes go through the app-host routes with the token, from the CLI or an agent.

## Rate limits

Hardcoded in nginx for now, per visitor (needs `real_ip` behind the tunnel): about 20 reads per second with a burst of 40, and 2 writes per second with a burst of 10. Writes are keyed with a `map` on the request method so reads and writes use separate zones. These values live in the nginx template that `run.sh` generates.

## Build order

Fifteen small modules in three milestones. Each one ends with its tests passing and a stop for review. Nothing is committed unless asked. Sizes: S is under about 100 lines, M a few hundred, L the biggest pieces.

### A. Accounts

| # | Module | Delivers | Tests | Size |
|---|---|---|---|---|
| 1 | Migration runner | `migrate.go`: takes an `fs.FS` of numbered files, version check, `BEGIN IMMEDIATE`, pre-migration backup, `foreign_keys` off and on around the loop with `foreign_key_check`, a read-only status function. The connection string turns `foreign_keys` on. | Fixture migrations: fresh database, a second start applies nothing, a failure rolls back, gaps, a newer database, an old development database, two processes at once | M |
| 2 | Baseline schema | `0001_init.sql` from `migrations_plan.md`, the `//go:embed`, the runner wired into `openDB` in place of the old `CREATE TABLE IF NOT EXISTS` string, and `housepaged migrate status` | Every table exists, the cascade works, the two superuser triggers hold, demotion downgrades tokens | S |
| 3 | Users and tokens store | `db.go` functions to create, look up, list and revoke; user-name and label rules in `internal/rules` | Label uniqueness, a superuser token for an ordinary user is refused, revoking | M |
| 4 | Operator commands | `housepaged user create/set`, `token create/list/revoke`, the default token minted with a new user | Command-level tests | M |
| 5 | Publish API on users | `authenticate` returns the caller; publish, list and delete keyed by user id; the **site cap** inside the claim transaction | Existing tests adapted, the 11th site is refused, a republish still works, concurrent claims, deleting frees a slot | M |
| 6 | Users and tokens API | `POST/GET/PUT /users`, `POST/GET/DELETE /tokens`, the JSON error helper | The whole minting matrix, the default token on user creation | L |
| 7 | Client token commands | `housepage token create/list/revoke` | Against a fake server, like the existing client tests | S |

Checkpoint: accounts are complete, the data API is not started.

### B. The data API

| # | Module | Delivers | Tests | Size |
|---|---|---|---|---|
| 8 | Data rules and cursor codec | Collection, key and sort-path rules, defaults and ceilings, cursor encode and decode (pure functions) | Good and bad inputs, round trips | S |
| 9 | Document store, writes | ULID generator (no new dependency), get, put, insert, replace, delete, the derived cap checks, per-site settings | Each primitive, each cap, an emptied collection frees its slot, document size | L |
| 10 | Document store, listing | Default order, `sort`, missing values last, cursors, `limit + 1` | Insert before the cursor gives no duplicates, nested sort, mismatched cursor, last page | L |
| 11 | Data API handlers | Shared handlers, mode checks, JSON errors, both mounts (app host, and the site host with `X-Housepage-Site`) | Mode x entry point x route matrix | L |
| 12 | Settings API and client | `GET/PUT db-config`, `housepage db config` and `db show` | Partial updates, ceilings, usage numbers | M |

Checkpoint: the data API works through Go tests.

### C. Edge, agents, docs

| # | Module | Delivers | Tests | Size |
|---|---|---|---|---|
| 13 | nginx and `run.sh` | The `/_api/` location, the site header, rate-limit zones, JSON 429, the 16 KB body limit | `--render-only`, `nginx -t`, then a local end-to-end run through nginx including a spoofed header and the 429 | M |
| 14 | Skill | A data section in `SKILL.md` | An agent run | S |
| 15 | Docs and loose ends | README, `CLAUDE.md`, `run.sh`'s first-run hints (`user create --superuser`), the Code Tour artifact (stale after module 5) | | S |

```
1 -> 2 -> 3 -+-> 4
             +-> 5 ----------------+
             +-> 6 -> 7            |
8 -> 9 -> 10 -> 11 <---------------+   (needs the caller from 5 and the store from 3)
                 +-> 12 -> 13 -> 14 -> 15
```

Module 1 does not embed any files: a `//go:embed` pattern that matches nothing does not compile. The runner therefore takes an `fs.FS` and is tested with fixture files. The embed arrives with `0001_init.sql` in module 2.

Decided: a user may revoke the token they are currently using (the CLI warns first, and the operator can always mint a new one), and only the operator can revoke other users' tokens, not a superuser.

Decided: the name rules, page sizes and error shapes are the ones in `api-contracts.md`, which is the reference for them.

## Future work

- **Read and write rates per second in the database.** Today they are hardcoded in nginx and are fine. Later they may become per-site settings stored with the other limits (`site_db_config`) and enforced by housepaged, so owners could tune them like the document caps. Revisit when a second person uses the platform or when one site needs different rates.
- The `increment` strategy (atomic add on a number), so shared counters never lose updates, then `merge`.
- Indexes: declared per site, or created automatically for sort keys that are used a lot, if sorted listing becomes slow at larger caps. Measure on the Pi first.
- Per-collection access rules (for example `events` open, `config` owner-only).
- Private reads, which need viewer login (Cloudflare Access or our own).
- Realtime `subscribe` over Server-Sent Events (named tunnels support it; quick tunnels do not).
- A counter table, only if the per-collection count (about 17 ms at 20,000 documents on a Mac) shows up as a problem.
- A per-site total size limit, if the 8 GB theoretical ceiling turns out to be a problem.
- Not planned: a keys-only listing, offset pagination.
