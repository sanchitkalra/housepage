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

Each step ends with passing tests and a check-in before the next one.

| # | Build | Tests |
|---|---|---|
| 0 | Migration runner, users and tokens: numbered `.sql` files embedded in the binary and applied at startup, tracked with `PRAGMA user_version`. The baseline `0001_init.sql` creates the whole schema in the diagram. Foreign keys on, the `users` table with `is_superuser`, `tokens` with labels and their own `is_superuser` flag plus the triggers that guard it, `sites.owner_id`, the site cap checked inside the claim transaction, the operator commands (`user create`, `user set`, `token create`, `token list`, `token revoke`), the client commands `housepage token create|list|revoke`, and the API routes for users and tokens. Existing publish, list and delete keep their behaviour, now keyed by the user's id. | Migrations apply in order and exactly once (a second startup applies nothing), a failing migration rolls back and stops startup, and an old-schema development database gets the clear error. The 11th site is refused with 403 and a republish still works. Two concurrent claims cannot exceed the cap. Deleting a site frees a slot. Superuser tokens can only be created for superusers (the trigger rejects the rest), and demoting a user downgrades their superuser tokens. The minting rules hold: an ordinary token can mint only ordinary tokens for its own user, a superuser token can create users and mint for others, and the operator can do anything. Labels are unique per user. Listing never shows hashes or plaintext, a revoked token stops working at once, and revoking another user's token returns 404 (the operator can revoke any). A superuser token can change a limit and an ordinary token gets 403. |
| 1 | `internal/rules`: collection and key name rules, sort-path rule, limit defaults and ceilings | Good and bad inputs for each rule |
| 2 | `cmd/housepaged/docstore.go`: tables, `Get`, `Put`, `Insert` (small ULID generator, no new dependency), `Update` (`replace`), `Delete`, `List` with cursors, limit checks, derived counts | Insert-before-the-cursor gives no duplicates, nulls last, nested sort, cursor with mismatched sort is rejected, each cap, emptying a collection frees its slot, document size |
| 3 | `cmd/housepaged/dbapi.go`: shared handlers, mode checks, JSON errors, both mounts | Per mode (off, open, owner) x per entry point x per route status code |
| 4 | `db-config` routes and the CLI commands `db config` and `db show` | Setting, partial setting, ceilings, show output |
| 5 | nginx: `/_api/` location, `X-Housepage-Site`, two rate-limit zones, in the `run.sh` template. Check with `--render-only` and `nginx -t` | A local end-to-end run through nginx like the earlier ones, including a spoofed header and the 429 |
| 6 | `SKILL.md`: a data section so agents know how pages call the API | Agent run, as before |
| 7 | README, `CLAUDE.md` and the closing hints printed by `run.sh` updated (the first run becomes `housepaged user create <name> --superuser`) | |

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
