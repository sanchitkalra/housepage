-- housepage baseline schema. The same shape as db-diagram.dbml.
-- Until the first real deploy this file may be edited in place; after that, a change means a new numbered file.
-- The server ceilings (collections, documents, document size) are enforced by housepaged, not here.

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
