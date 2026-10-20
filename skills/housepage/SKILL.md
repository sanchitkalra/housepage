---
name: housepage
description: Publish a static website or PWA to housepage so it gets a public URL (NAME.<domain>). Use when the user asks to publish, deploy, host, put online or share a page, app or PWA, or to list or delete sites already published. Also use when a published page needs data saved and shared between visitors (guestbook, votes, scores, comments, a shared list) or the user asks about a site's database.
---

# Publishing with housepage

`housepage` uploads a folder of static files. The site goes live at `NAME.<domain>` within a second; the URL that `publish` prints is the authoritative one. Sites are static only: HTML, CSS, JS, images, a PWA manifest and service worker. There is no server-side code. State that only one browser needs goes in localStorage or IndexedDB; state shared between visitors goes in the site data store described below.

## Before publishing

1. Run `housepage list`. If it says `not logged in`, stop and ask the user to run `housepage login --server URL --token TOKEN` themselves. Never invent or guess a token.
2. Publish a **dedicated output folder** (for example `dist/` or `site/`), not a repo root. Look at what is in it first (`ls -R`). Everything in the folder becomes public, so never publish `.env` files, keys, or source you did not mean to share.
3. The folder must contain `index.html` at its top level.

## Commands

| Task | Command |
|---|---|
| Publish or update | `housepage publish <dir> --name <name>` |
| List your sites | `housepage list` |
| Delete a site | `housepage delete <name>` |

- `publish` prints the live URL as its only stdout line. Always tell the user that URL.
- Publishing under a name you already own **replaces the whole site**, so include every file each time. Files you leave out are removed.
- Only run `delete` when the user asks for it. It cannot be undone.

## Names and limits

- Name: 3-63 characters, lowercase letters, digits and hyphens, not starting or ending with a hyphen. Reserved: `app`, `www`, `api`, `admin`, `mail`.
- Max 500 files, 5 MB per file, 25 MB total. Symlinks are rejected. `.git` and `.DS_Store` are skipped.
- Pick a short descriptive name from what the site is (`tip-calculator`, not `my-site-1`).

## Errors

| Message contains | Do this |
|---|---|
| `already taken` | The name belongs to someone else. Choose a different name; do not retry the same one. |
| `index.html is required` | Add `index.html` at the folder root. |
| `invalid name` / `invalid path` | Fix the name or file path as the message says. |
| `too large` / `limit is` | Shrink or compress assets, or drop files that are not needed. |
| `401` / `not logged in` | Ask the user to log in. |
| `above the server ceiling` | A `db config` limit is too high; use a value at or under the ceiling the message names. |
| `429` / `too many requests` | Wait about 10 seconds and retry once. Do not loop. |

## Shared data (optional)

A published site can keep small JSON data that its own page reads and writes with `fetch('/_api/...')`. It is same-origin: no token, no CORS, no secret in the page. `/_api/` exists only on the published site, so test it on the live URL, not from a local file.

**It is off until you turn it on**, and the site must be published first:

| Task | Command |
|---|---|
| Visitors may read and write | `housepage db config <site> --mode open` |
| Visitors may read only | `housepage db config <site> --mode owner` |
| Show mode, limits and usage | `housepage db show <site>` |
| Raise a limit (flags change only what is given) | `housepage db config <site> --max-docs 2000` (also `--max-collections`, `--max-doc-size`; the value `default` restores the default) |

`--mode open` means **anyone with the URL can add, change and delete the data**. **Stop and ask first**: do not run `--mode open` until the user has said yes in this conversation, even if the page cannot work without it. Publish, explain what open mode allows, ask, and enable it only after the answer. Never store secrets or personal data. `--mode owner` lets visitors read but not write; there is no command to write data yourself, so use it only for data that is already there.

**Two stores**, both returning JSON. A document always comes wrapped as `{id, createdAt, updatedAt, data}` (your JSON is in `data`) and a list as `{items, nextCursor}`.

| Store | Calls |
|---|---|
| Key-value (settings, one value per key, any JSON) | `PUT /_api/kv/{key}` (201 created, 200 replaced), `GET /_api/kv/{key}`, `DELETE /_api/kv/{key}`, `GET /_api/kv?prefix=&limit=&cursor=` |
| Collections (many records, each an object) | `POST /_api/db/{collection}` (the server makes the id, 201), `GET /_api/db/{collection}/{id}`, `PUT /_api/db/{collection}/{id}` (replaces; 404 if the id is missing, never creates), `DELETE /_api/db/{collection}/{id}`, `GET /_api/db/{collection}?sort=&order=&limit=&cursor=` |

```js
await fetch('/_api/db/messages', { method: 'POST', body: JSON.stringify({ name, text }) });
const { items, nextCursor } = await (await fetch('/_api/db/messages?order=desc&limit=20')).json();
```

- Keys: 1-128 of letters, digits and `. _ : -`. Collections: 1-64 of lowercase letters, digits, `_` and `-`, not starting with `_`. A collection exists while it has documents.
- **Listing.** Default is oldest first; `order=desc` is newest first. `sort=score&order=desc` sorts by a field (nested works: `sort=when.start`); documents without the field come last. `limit` is 1-100 (default 20). For the next page send only `?cursor=<nextCursor>` (and `limit` if you like) until `nextCursor` is null. There are no page numbers or offsets.
- **Limits** per site: 5 collections, 500 documents per collection, 500 key-value entries, 4096 bytes per document or value. Over a limit the call fails with 409 `quota_exceeded` or 413 `document_too_large`; the page should show a message, not break.
- **Errors** are JSON `{error, message}`: 403 `db_disabled` (not enabled) or `read_only`, 400 for a bad name or body, 404 `not_found`, 429 `rate_limited`. Each visitor gets about 20 reads and 2 writes per second, so never write on every keystroke.
- **No atomic updates.** `PUT` overwrites, so two visitors changing one document lose one change. Prefer adding a document per event (a vote, a score, a comment) and counting or sorting them, over updating one shared counter.
- Visitors can store anything, so render stored text with `textContent`, never `innerHTML`.

## If the site is a PWA

Each site has its own origin, so root-relative paths like `/sw.js` work.

- `manifest.webmanifest` with `name`, `short_name`, `start_url: "/"`, `display: "standalone"`, and PNG icons of 192x192 and 512x512. Link it from `index.html` with `<link rel="manifest" href="/manifest.webmanifest">`.
- `sw.js` at the folder root, registered with `navigator.serviceWorker.register('/sw.js')`.
- If the service worker caches files, put a version in the cache name and **change it on every publish**. Otherwise users keep seeing the old version.
- HTTPS is provided by the host. Do not add any redirect logic.
