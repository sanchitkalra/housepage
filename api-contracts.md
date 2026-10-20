# housepage API contracts

Every HTTP endpoint with a sample request and response. Domain `example.dev` and token `hp_9f2c…` are placeholders.

| Status | Section |
|---|---|
| **Built** | 2. Publishing sites |
| **Designed, not built** | 3. Users and tokens, 4. Site data settings, 5. Key-value, 6. Collections (see `site-data_plan.md`) |

All the details below, including the names, limits and error shapes, are approved.

## 1. Conventions

**Two hosts.**

| Host | Used by | Prefix |
|---|---|---|
| `app.example.dev` | the `housepage` CLI, agents, scripts (the owner) | `/api/v1/` |
| `<site>.example.dev` | the site's own JavaScript | `/_api/` (only the data routes, sections 5 and 6) |

The data routes exist on both. On the app host they live under `/api/v1/sites/<site>/…` and always need the owner's token. On a site host they live under `/_api/…`, need no token, and the site's data mode decides what is allowed (section 7). Everything else (publishing, users, tokens, settings) is app-host only.

**Authentication.** `Authorization: Bearer hp_…`. Tokens are created per user and carry a label. A *superuser token* is required where stated.

**Data formats.**

- Request and response bodies are JSON, except the publishing upload (multipart). The server ignores the request's `Content-Type` and parses the body as JSON.
- Timestamps are unix milliseconds (integers), for example `1760900000000`.
- A document in a response is always wrapped in the **envelope**:

```json
{ "id": "01JBX7M9Q2F5T8W3N4K6H1R0PD", "createdAt": 1760900000000, "updatedAt": 1760900000000, "data": { "title": "Launch" } }
```

- Lists are `{ "items": [ envelope, … ], "nextCursor": "…" | null }`.

**Errors.** The three publishing routes (section 2) answer errors in **plain text**, as they already do. Every other route answers errors in JSON:

```json
{ "error": "quota_exceeded", "message": "collection \"events\" is full: 500 of 500 documents" }
```

`error` is a stable code for programs, `message` is for people. The codes are in section 8. This applies to all the new routes, not only the data ones.

**Names.**

| Thing | Rule |
|---|---|
| Site | 3 to 63 characters of `a-z 0-9 -`, no leading or trailing hyphen, no `xn--` prefix. Reserved: `app www api admin mail ns1 ns2`. (Built.) |
| Collection | 1 to 64 characters of `a-z 0-9 _ -`, not starting with `_` (`_kv` is reserved) |
| KV key | 1 to 128 characters of `A-Z a-z 0-9 . _ : -` |
| Token label | 1 to 64 characters of `A-Z a-z 0-9 . _ -`, unique per user |
| User name | 1 to 64 characters, no control characters, not blank, unique |
| Sort path | letters, digits, `_` and `.` (for example `when.start`) |

**Limits.**

| | Default | Ceiling (server-wide) |
|---|---|---|
| Collections per site | 5 | 100 |
| Documents per collection (and entries in KV) | 500 | 20,000 |
| Size of one document or KV value | 4096 bytes | 4096 bytes |
| Sites per user | 10 | none, set by the operator |
| Files per published site | 500 | |
| Size of one published file / whole site | 5 MB / 25 MB | |
| `limit` on a list | 20 | 100 |

---

## 2. Publishing sites (built)

### `PUT /api/v1/sites/{name}`

Publishes a site, replacing whatever was there. Needs any token. The first publish of a name claims it for the token's user.

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" \
  -F "files=@index.html;filename=index.html" \
  -F "files=@css/app.css;filename=css/app.css" \
  https://app.example.dev/api/v1/sites/tip
```

Each part's `filename` is the file's relative path. Response `200`:

```json
{ "url": "https://tip.example.dev/" }
```

| Status | Plain-text body |
|---|---|
| 400 | `expected a multipart/form-data body` |
| 400 | `index.html is required at the site root` |
| 400 | `invalid name "Bad_Name": use 3-63 lowercase letters, digits or hyphens, not starting or ending with a hyphen` |
| 400 | `invalid path "../x": must be relative, with no empty, . or .. segments` |
| 401 | `missing or invalid token` (with `WWW-Authenticate: Bearer`) |
| 403 | `name "tip" is already taken` |
| 403 | `site limit reached: 10 of 10` *(new with users; republishing a site you already own never hits it)* |
| 413 | `upload too large; the limit is 26214400 bytes` |

### `GET /api/v1/sites`

Lists the caller's own sites. Response `200`:

```json
[ { "name": "tip", "url": "https://tip.example.dev/" }, { "name": "notes", "url": "https://notes.example.dev/" } ]
```

### `DELETE /api/v1/sites/{name}`

Deletes a site, frees its name and (once the data API exists) deletes all its data. Response `200`:

```json
{ "deleted": "tip" }
```

| Status | Plain-text body |
|---|---|
| 400 | the invalid-name message |
| 403 | `site "tip" belongs to someone else` |
| 404 | `no site named "tip"` |

---

## 3. Users and tokens (designed)

### `POST /api/v1/users`

Creates a user. **Superuser token required.** The response also carries the user's first token, labelled `default`, shown only this once. A superuser account gets a superuser `default` token, an ordinary account an ordinary one.

```bash
curl -X POST -H "Authorization: Bearer $SUPER" -H "Content-Type: application/json" \
  -d '{"name":"sam","superuser":false}' https://app.example.dev/api/v1/users
```

Response `201`:

```json
{
  "user":  { "id": 2, "name": "sam", "isSuperuser": false, "maxSites": 10, "createdAt": 1760900000000 },
  "token": { "label": "default", "token": "hp_3b7e…", "isSuperuser": false, "createdAt": 1760900000000 }
}
```

`maxSites` is always the effective number (the default 10 when none is set).

| Status | `error` |
|---|---|
| 400 | `invalid_user_name` |
| 401 | `unauthorized` |
| 403 | `forbidden` (the token is not a superuser token) |
| 409 | `user_exists` |

### `GET /api/v1/users/{name}`

Reads a user. A user may read themselves, and a superuser token may read anyone. Response `200`:

```json
{ "id": 2, "name": "sam", "isSuperuser": false, "maxSites": 10, "sitesUsed": 3, "createdAt": 1760900000000 }
```

Errors: `401 unauthorized`, `403 forbidden` (someone else's user with an ordinary token), `404 not_found`.

### `PUT /api/v1/users/{name}`

Changes a user's limits. **Superuser token required**; users cannot change their own. Only the fields sent change. `null` restores the default.

```json
{ "maxSites": 25 }
```

Response `200`: the user, as in `GET`. Errors: `400 invalid_request` (negative or non-integer), `401`, `403 forbidden`, `404 not_found`.

`isSuperuser` is not changeable through the API in v0. Promoting or demoting is an operator action on the server (`housepaged user set <user> --superuser | --no-superuser`).

### `POST /api/v1/tokens`

Mints a token **for the caller's own user**. Ordinary tokens can mint ordinary tokens. Minting a superuser token needs a superuser token.

```json
{ "label": "ci", "superuser": false }
```

Response `201` (the plaintext appears only here):

```json
{ "label": "ci", "token": "hp_c41d…", "isSuperuser": false, "createdAt": 1760900100000 }
```

| Status | `error` |
|---|---|
| 400 | `invalid_label` |
| 403 | `forbidden` (`superuser: true` from an ordinary token) |
| 409 | `label_exists` |

### `GET /api/v1/tokens`

Lists the caller's own tokens. Never returns the hash or the plaintext. `current` marks the token used for this request.

```json
[
  { "label": "default", "isSuperuser": false, "createdAt": 1760900000000, "current": true },
  { "label": "ci",      "isSuperuser": false, "createdAt": 1760900100000, "current": false }
]
```

### `DELETE /api/v1/tokens/{label}`

Revokes one of the caller's own tokens. It stops working at once, including a token that is revoking itself (the CLI warns first). Response `204`. Errors: `404 not_found` (also for someone else's label).

---

## 4. Site data settings (designed)

### `GET /api/v1/sites/{site}/db-config`

The site's data mode, effective limits, the server ceilings and current usage. Owner token only.

```json
{
  "site": "tip",
  "mode": "open",
  "limits":   { "maxCollections": 5,   "maxDocs": 500,   "maxDocSize": 4096 },
  "ceilings": { "maxCollections": 100, "maxDocs": 20000, "maxDocSize": 4096 },
  "usage":    { "collections": 2, "documentsByCollection": { "events": 120, "notes": 3 }, "kvEntries": 7 }
}
```

A site that was never configured reads `"mode": "off"` with the default limits.

### `PUT /api/v1/sites/{site}/db-config`

Sets only the fields sent: `mode` (`off`, `open`, `owner`), `maxCollections`, `maxDocs`, `maxDocSize`. `null` restores a default. Response `200`: the same body as `GET`.

```json
{ "mode": "open", "maxDocs": 1000 }
```

| Status | `error` |
|---|---|
| 400 | `above_ceiling`: `maxDocs 30000 is above the server ceiling of 20000` |
| 400 | `invalid_request`: unknown mode, negative number |
| 401 | `unauthorized` |
| 403 | `forbidden`: the site belongs to someone else |
| 404 | `not_found`: no such site |

Lowering a limit below current usage is accepted. Existing data stays, and only new inserts are refused until usage is back under it.

---

## 5. Key-value (designed)

The routes below are shown on the site host (`/_api/…`). On the app host the same routes sit under `/api/v1/sites/<site>/…`.

### `PUT /_api/kv/{key}`

Creates or replaces a value. The body is any JSON value. Response `201` when created, `200` when replaced.

```bash
curl -X PUT https://tip.example.dev/_api/kv/score -d '42'
```

```json
{ "id": "score", "createdAt": 1760900000000, "updatedAt": 1760900000000, "data": 42 }
```

An optional `?strategy=replace` is accepted (and is the default). Any other strategy is a `400 unknown_strategy`.

### `GET /_api/kv/{key}`

Response `200`: the envelope above. `404 not_found` if the key does not exist.

### `DELETE /_api/kv/{key}`

Response `204`, also when the key was not there.

### `GET /_api/kv?prefix=&limit=&cursor=`

Lists entries in **key order**. `prefix` is optional.

```bash
curl "https://tip.example.dev/_api/kv?prefix=score:&limit=2"
```

```json
{
  "items": [
    { "id": "score:amy", "createdAt": 1760900000000, "updatedAt": 1760900500000, "data": 31 },
    { "id": "score:bob", "createdAt": 1760900200000, "updatedAt": 1760900200000, "data": 18 }
  ],
  "nextCursor": "eyJpIjoic2NvcmU6Ym9iIiwicCI6InNjb3JlOiJ9"
}
```

| Status | `error` |
|---|---|
| 400 | `invalid_key`, `invalid_json`, `invalid_cursor`, `unknown_strategy` |
| 403 | `db_disabled`, `read_only` (section 7) |
| 409 | `quota_exceeded`: `the key-value store is full: 500 of 500 entries` |
| 413 | `document_too_large`: `value is 5120 bytes; the limit is 4096` |

---

## 6. Collections (designed)

A collection is an ordered set of JSON documents, each with a server-generated id (a ULID, so id order is creation order). Collections are created by the first insert and disappear when their last document is deleted.

### `POST /_api/db/{collection}`

Inserts a document. The body must be a JSON **object**. Response `201`, including the new id:

```bash
curl -X POST https://tip.example.dev/_api/db/events -d '{"title":"Launch","when":{"start":"2026-03-04"}}'
```

```json
{
  "id": "01JBX7M9Q2F5T8W3N4K6H1R0PD", "createdAt": 1760900000000, "updatedAt": 1760900000000,
  "data": { "title": "Launch", "when": { "start": "2026-03-04" } }
}
```

| Status | `error` |
|---|---|
| 400 | `invalid_collection`, `invalid_json`, `not_an_object` |
| 409 | `quota_exceeded`: `collection "events" is full: 500 of 500 documents`, or `site has 5 of 5 collections; "tasks" would be a sixth` |
| 413 | `document_too_large`: `document is 5120 bytes; the limit is 4096` |

### `GET /_api/db/{collection}/{id}`

Response `200`: the envelope. `404 not_found` otherwise.

### `PUT /_api/db/{collection}/{id}?strategy=replace`

Replaces the document's `data` with the body, which must be an object. `strategy` is optional (default `replace`, the only one in v0). There is **no upsert**: an id that does not exist is a `404 not_found`. Response `200`: the updated envelope, with a new `updatedAt`. Errors: as for `POST`, plus:

```json
{ "error": "unknown_strategy", "message": "unknown strategy \"merge\"; valid strategies: replace" }
```

### `DELETE /_api/db/{collection}/{id}`

Response `204`, also when the id was not there.

### `GET /_api/db/{collection}?sort=&order=&limit=` and `?cursor=`

Lists full documents. There is no `offset` and there are no page numbers: **paging is by cursor only**.

| Parameter | Meaning |
|---|---|
| `sort` | optional. A path into the document, nested allowed (`when.start`). Default is creation order. |
| `order` | `asc` (default) or `desc` |
| `limit` | page size |
| `cursor` | the `nextCursor` of the previous page. Send **only** this (and optionally `limit`) on later pages. |

Documents without a value for the sort path come **last**, in either direction. Text values compare on their first 256 characters. A document is considered to come "before" or "after" your position by value, so documents added earlier in the order after you started paging do not appear until you start again from the first page.

First page, sorted:

```bash
curl "https://tip.example.dev/_api/db/events?sort=when.start&order=asc&limit=2"
```

```json
{
  "items": [
    { "id": "01JBX7M9Q2…", "createdAt": 1760900000000, "updatedAt": 1760900000000, "data": { "title": "Kickoff", "when": { "start": "2026-01-12" } } },
    { "id": "01JBX7MA4D…", "createdAt": 1760900300000, "updatedAt": 1760900300000, "data": { "title": "Launch",  "when": { "start": "2026-03-04" } } }
  ],
  "nextCursor": "eyJzIjoid2hlbi5zdGFydCIsIm8iOiJhc2MiLCJuIjowLCJ2IjoiMjAyNi0wMy0wNCIsImkiOiIwMUpCWDdNOVEyRjVUOFczTjRLNkgxUjBQRCJ9"
}
```

Next page (the cursor alone):

```bash
curl "https://tip.example.dev/_api/db/events?cursor=eyJzIjoid2hlbi5zdGFydCIs…"
```

The last page has `"nextCursor": null`. Cursors are opaque: do not parse them. They are not consumed, so an old cursor can be sent again.

| Status | `error` |
|---|---|
| 400 | `invalid_cursor`, `invalid_sort`, `invalid_order`, `invalid_limit` |
| 400 | `cursor_mismatch`: `this cursor was issued for sort=when.start order=asc; start again without a cursor to change the sort` (sent only when `sort` or `order` accompany a cursor and differ from it) |

---

## 7. Who may do what with site data

| Site data mode | A page reads (`GET /_api/…`) | A page writes (`POST`, `PUT`, `DELETE`) | The owner with a token (app host) |
|---|---|---|---|
| `off` (default) | `403 db_disabled` | `403 db_disabled` | allowed |
| `open` | allowed | allowed | allowed |
| `owner` | allowed | `403 read_only` | allowed |

Unlike the rest of the platform, a page's request carries no identity: the site is identified by the host name that nginx saw, and nginx passes it on in an internal header that a visitor cannot forge.

## 8. Error codes

| `error` | Status | Meaning |
|---|---|---|
| `invalid_request`, `invalid_json`, `not_an_object`, `invalid_user_name`, `invalid_label`, `invalid_key`, `invalid_collection`, `invalid_sort`, `invalid_order`, `invalid_limit`, `invalid_cursor` | 400 | The request is malformed. The message says what. |
| `cursor_mismatch`, `unknown_strategy`, `above_ceiling` | 400 | The request is well-formed but not allowed as asked |
| `unauthorized` | 401 | Missing or unknown token |
| `forbidden` | 403 | The token may not do this (not a superuser, not the owner) |
| `db_disabled`, `read_only` | 403 | The site's data mode does not allow it |
| `not_found` | 404 | No such user, token, site, key or document |
| `user_exists`, `label_exists` | 409 | The name is taken |
| `quota_exceeded` | 409 | Too many documents, collections or KV entries |
| `document_too_large` | 413 | The document or value is over the size limit |
| `rate_limited` | 429 | Sent by nginx, see below |

## 9. Responses that come from nginx, not from housepaged

| Situation | Status | Note |
|---|---|---|
| Too many requests | 429 | About 20 reads and 2 writes per second per visitor, with bursts. On `/_api/` the body is JSON, `{"error":"rate_limited",…}`. Elsewhere nginx's default body is HTML, so go by the status code. |
| Request body too large | 413 | 30 MB on the app host, 16 KB on `/_api/` (a document is at most 4 KB). |
| Anything else on a site host that is not a file | 404 | nginx's own page |

## 10. Cross-origin behaviour

The data routes are meant for same-origin use by the site's own page (`fetch('/_api/kv/score')`), so no CORS headers are sent. A page on another origin cannot read the responses. Because there are no cookies and no ambient credentials, a request from another origin cannot do anything the visitor could not do directly.
