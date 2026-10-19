---
name: housepage
description: Publish a static website or PWA to housepage so it gets a public URL (NAME.<domain>). Use when the user asks to publish, deploy, host, put online or share a page, app or PWA, or to list or delete sites already published.
---

# Publishing with housepage

`housepage` uploads a folder of static files. The site goes live at `NAME.<domain>` within a second; the URL that `publish` prints is the authoritative one. Sites are static only: HTML, CSS, JS, images, a PWA manifest and service worker. There is no server-side code or database. Use localStorage or IndexedDB for state.

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
| `429` / `too many requests` | Wait about 10 seconds and retry once. Do not loop. |

## If the site is a PWA

Each site has its own origin, so root-relative paths like `/sw.js` work.

- `manifest.webmanifest` with `name`, `short_name`, `start_url: "/"`, `display: "standalone"`, and PNG icons of 192x192 and 512x512. Link it from `index.html` with `<link rel="manifest" href="/manifest.webmanifest">`.
- `sw.js` at the folder root, registered with `navigator.serviceWorker.register('/sw.js')`.
- If the service worker caches files, put a version in the cache name and **change it on every publish**. Otherwise users keep seeing the old version.
- HTTPS is provided by the host. Do not add any redirect logic.
