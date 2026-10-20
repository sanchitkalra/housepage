# Migrations: how they run and how they are managed

Design only, approved. Nothing here is implemented yet; it is step 0 of `site-data_plan.md`. It replaces the `CREATE TABLE IF NOT EXISTS` string in `db.go`, which can create tables but never change one.

## The idea in one paragraph

The database schema is a list of numbered SQL files compiled into the `housepaged` binary. On every start, `housepaged` reads the database's current version and applies **only the files numbered higher than it, each exactly once**, in order, each inside one transaction, recording the new version as part of that transaction. A migration never re-runs: a fresh database runs them all, an up-to-date one runs none (the start only reads one integer), and an old one runs only what it is missing. Nothing is deployed yet, so there is a single file, `0001_init.sql`, holding the whole schema from `db-diagram.dbml`.

## Layout

```
cmd/housepaged/
  migrations/
    0001_init.sql          the baseline: the whole schema
    0002_<what>.sql        future changes, one file each
  migrate.go               the runner (about 80 lines)
  migrate_test.go
```

The files are embedded with `//go:embed migrations/*.sql`, so there is nothing to ship next to the binary and the Pi needs only the executable.

Rules for the files:

| Rule | Why |
|---|---|
| Named `NNNN_description.sql`, four digits, starting at `0001`, no gaps, no duplicates | The runner checks this at startup and refuses to run on a gap |
| Forward only: there are no down migrations | A bad release is undone by restoring the pre-migration backup (below), not by reverse SQL |
| Until the first real deploy, `0001` may be edited in place | There is no data to protect, and a development database is simply deleted |
| After the first deploy, an applied file is never edited | A change always means a new, higher-numbered file |

## What happens at startup

Every entry point opens the database through the same function, so `housepaged serve` and the operator commands (`user create`, `token create`, and so on) all bring the schema up to date before doing anything else.

```
open the database (foreign keys on)
read  PRAGMA user_version                        -> current   (0 for a brand-new file)
list  the embedded files                         -> check numbering: 1, 2, 3 ... with no gap
if current > the highest embedded number         -> stop: "database is version 7 but this binary only knows 5; upgrade housepaged"
if current == 0 and the database already has tables
                                                 -> stop: "this database was created by an earlier development version; delete <path> and start again"
if there is nothing pending                      -> done
otherwise:
    if current >= 1: copy the file to housepage.db.before-<next>.bak      (see "Backups")
    PRAGMA foreign_keys = OFF
    for each pending file n, in order:
        BEGIN IMMEDIATE                           -- takes the write lock, so two starting processes queue up
        re-read user_version; if another process already applied n, COMMIT and skip
        run the file's SQL
        PRAGMA foreign_key_check                  -- must return no rows, otherwise ROLLBACK and stop
        PRAGMA user_version = n
        COMMIT
        log "migration-applied | version=n | file=0002_x.sql"
    PRAGMA foreign_keys = ON
```

Why the odd-looking parts are there:

| Piece | Reason |
|---|---|
| Each file in one transaction, with `user_version` set inside it | A failed file rolls back completely, including the version number. The database is never left half-migrated. |
| `BEGIN IMMEDIATE` and the re-read | The operator may run `housepaged token create` while the service restarts. Both open the database, and exactly one of them applies a migration. |
| `foreign_keys = OFF` around the loop, `foreign_key_check` before commit | SQLite cannot change a table in place for some edits (adding a foreign key, dropping a column). The documented way is to build a new table, copy the rows, drop the old one and rename it, which breaks foreign keys for a moment. This is the standard procedure for it, so future migrations can do it safely. |
| Refusing a database newer than the binary | A downgrade would otherwise run old code against a schema it does not understand |

## Failure behaviour

- A migration that fails rolls back and `housepaged` exits with a non-zero status and a log line naming the file and the SQL error.
- Under `run.sh` the supervisor then keeps restarting it, so you see a crash loop in `run/logs/housepaged.log`, and `run.sh`'s own check prints its warning. The database is unchanged, so fixing the file or restoring the binary and restarting is enough.

## Backups

Before applying pending migrations to an existing database, the runner copies the file to `housepage.db.before-0003.bak` (named after the first pending version). That is the rollback path: stop the service, put the copy back, run the older binary. It costs a file copy only when a migration is about to run, and SQLite files here are small. One backup per version is kept, and nothing is deleted automatically.

## Day to day

| Task | How |
|---|---|
| Add a schema change | Create `0002_<what>.sql` in `migrations/`, rebuild, restart. The runner applies it on start. |
| See where a database stands | `housepaged migrate status` prints the current version, the latest embedded one and the pending files, and changes nothing |
| Start over in development | Delete `housepage.db`. The next start rebuilds it from `0001`. |
| Test a migration | The tests build a fresh database, then (once there is a `0002`) build a database at version 1 and migrate it up |

## Tests

- A fresh database ends at the latest version, with every table, index and trigger from the diagram.
- A second start applies nothing.
- A file that fails halfway leaves the version and the schema exactly as they were (test uses a small fixture set of files, not the real ones).
- A gap or a duplicate number is refused.
- A database newer than the binary is refused.
- An old-schema development database (version 0 with tables present) gets the clear message.
- The two superuser triggers: a superuser token for an ordinary user is rejected on insert and on update, and demoting a user downgrades their tokens.
- Two processes starting at once apply each migration exactly once.

## The baseline: `0001_init.sql` (draft)

This is exactly the schema in `db-diagram.dbml`. The server-wide ceilings (100 collections, 20,000 documents, 4 KB) are deliberately not in the SQL: they are enforced by `housepaged`, so changing a ceiling never needs a migration.

```sql
CREATE TABLE users (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    is_superuser INTEGER NOT NULL DEFAULT 0 CHECK (is_superuser IN (0, 1)),
    max_sites    INTEGER CHECK (max_sites IS NULL OR max_sites >= 0),
    created_at   INTEGER NOT NULL
);

CREATE TABLE tokens (
    hash         TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    label        TEXT    NOT NULL,
    is_superuser INTEGER NOT NULL DEFAULT 0 CHECK (is_superuser IN (0, 1)),
    created_at   INTEGER NOT NULL,
    UNIQUE (user_id, label)
);

CREATE TABLE sites (
    name     TEXT    PRIMARY KEY,
    owner_id INTEGER NOT NULL REFERENCES users(id)
);
CREATE INDEX sites_owner ON sites(owner_id);

CREATE TABLE site_db_config (
    site            TEXT PRIMARY KEY REFERENCES sites(name) ON DELETE CASCADE,
    mode            TEXT NOT NULL DEFAULT 'off' CHECK (mode IN ('off', 'open', 'owner')),
    max_collections INTEGER CHECK (max_collections IS NULL OR max_collections >= 0),
    max_docs        INTEGER CHECK (max_docs IS NULL OR max_docs >= 0),
    max_doc_size    INTEGER CHECK (max_doc_size IS NULL OR max_doc_size >= 1)
);

CREATE TABLE docs (
    site       TEXT    NOT NULL REFERENCES sites(name) ON DELETE CASCADE,
    collection TEXT    NOT NULL,
    id         TEXT    NOT NULL,
    value      TEXT    NOT NULL CHECK (json_valid(value)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (site, collection, id)
) WITHOUT ROWID;

-- A superuser token may belong only to a superuser.
CREATE TRIGGER tokens_superuser_insert BEFORE INSERT ON tokens
WHEN NEW.is_superuser = 1 AND (SELECT is_superuser FROM users WHERE id = NEW.user_id) = 0
BEGIN
    SELECT RAISE(ABORT, 'superuser tokens can only belong to superusers');
END;

CREATE TRIGGER tokens_superuser_update BEFORE UPDATE OF is_superuser, user_id ON tokens
WHEN NEW.is_superuser = 1 AND (SELECT is_superuser FROM users WHERE id = NEW.user_id) = 0
BEGIN
    SELECT RAISE(ABORT, 'superuser tokens can only belong to superusers');
END;

-- Demoting a user downgrades their superuser tokens.
CREATE TRIGGER users_demote AFTER UPDATE OF is_superuser ON users
WHEN OLD.is_superuser = 1 AND NEW.is_superuser = 0
BEGIN
    UPDATE tokens SET is_superuser = 0 WHERE user_id = NEW.id;
END;
```

Notes on the draft:
- There is no seed data. The first user is created by the operator (`housepaged user create <name> --superuser`).
- Timestamps are unix milliseconds in plain integers.
- `ON DELETE CASCADE` only does anything with foreign keys switched on, which the connection setup does.

## Decisions

Approved.

1. **Version tracking:** `PRAGMA user_version`. A `schema_migrations` table with checksums can be added later if an applied file is ever edited by accident.
2. **Pre-migration backup:** kept, as described above.
3. **`housepaged migrate status`:** included.
