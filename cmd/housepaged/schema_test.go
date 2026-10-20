package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// schemaDB returns a database built from the real embedded migrations.
func schemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, path := testDB(t)
	if _, err := migrate(context.Background(), db, path, embeddedMigrations()); err != nil {
		t.Fatal(err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func mustFail(t *testing.T, db *sql.DB, want, query string, args ...any) {
	t.Helper()
	_, err := db.Exec(query, args...)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: got %v, want an error containing %q", query, err, want)
	}
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBaselineCreatesEverythingInTheDiagram(t *testing.T) {
	db := schemaDB(t)
	if version(t, db) != 1 {
		t.Errorf("version %d, want 1", version(t, db))
	}
	for _, table := range []string{"users", "tokens", "sites", "site_db_config", "docs"} {
		if !hasTable(t, db, table) {
			t.Errorf("table %s is missing", table)
		}
	}
	for _, name := range []string{"sites_owner", "tokens_superuser_insert", "tokens_superuser_update", "users_demote"} {
		if count(t, db, `SELECT count(*) FROM sqlite_master WHERE name = ?`, name) != 1 {
			t.Errorf("%s is missing", name)
		}
	}
}

func TestDeletingASiteDeletesItsDataAndSettings(t *testing.T) {
	db := schemaDB(t)
	mustExec(t, db, `INSERT INTO users(id, name, created_at) VALUES (1, 'sam', 0)`)
	mustExec(t, db, `INSERT INTO sites VALUES ('tip', 1), ('keep', 1)`)
	mustExec(t, db, `INSERT INTO site_db_config(site, mode) VALUES ('tip', 'open')`)
	mustExec(t, db, `INSERT INTO docs VALUES ('tip', 'events', 'a', '{}', 0, 0), ('tip', '_kv', 'k', '1', 0, 0), ('keep', 'events', 'a', '{}', 0, 0)`)

	mustExec(t, db, `DELETE FROM sites WHERE name = 'tip'`)

	if n := count(t, db, `SELECT count(*) FROM docs WHERE site = 'tip'`); n != 0 {
		t.Errorf("%d documents survived the site", n)
	}
	if n := count(t, db, `SELECT count(*) FROM site_db_config`); n != 0 {
		t.Errorf("%d settings rows survived the site", n)
	}
	if n := count(t, db, `SELECT count(*) FROM docs WHERE site = 'keep'`); n != 1 {
		t.Errorf("another site's document was deleted: %d left", n)
	}
}

func TestForeignKeysRefuseOrphans(t *testing.T) {
	db := schemaDB(t)
	mustExec(t, db, `INSERT INTO users(id, name, created_at) VALUES (1, 'sam', 0)`)
	mustExec(t, db, `INSERT INTO sites VALUES ('tip', 1)`)
	mustFail(t, db, "FOREIGN KEY", `INSERT INTO sites VALUES ('ghost', 99)`)
	mustFail(t, db, "FOREIGN KEY", `INSERT INTO tokens(hash, user_id, label, created_at) VALUES ('h', 99, 'x', 0)`)
	mustFail(t, db, "FOREIGN KEY", `INSERT INTO docs VALUES ('nosuchsite', 'c', 'i', '{}', 0, 0)`)
	mustFail(t, db, "FOREIGN KEY", `INSERT INTO site_db_config(site) VALUES ('nosuchsite')`)
	mustFail(t, db, "FOREIGN KEY", `DELETE FROM users WHERE id = 1`) // still owns a site
}

func TestColumnRules(t *testing.T) {
	db := schemaDB(t)
	mustExec(t, db, `INSERT INTO users(id, name, created_at) VALUES (1, 'sam', 0)`)
	mustExec(t, db, `INSERT INTO sites VALUES ('tip', 1)`)
	mustExec(t, db, `INSERT INTO tokens(hash, user_id, label, created_at) VALUES ('h1', 1, 'laptop', 0)`)

	mustFail(t, db, "UNIQUE", `INSERT INTO users(name, created_at) VALUES ('sam', 0)`)
	mustFail(t, db, "UNIQUE", `INSERT INTO tokens(hash, user_id, label, created_at) VALUES ('h2', 1, 'laptop', 0)`) // same label, same user
	mustFail(t, db, "CHECK", `INSERT INTO users(name, is_superuser, created_at) VALUES ('x', 2, 0)`)
	mustFail(t, db, "CHECK", `INSERT INTO site_db_config(site, mode) VALUES ('tip', 'public')`)
	mustFail(t, db, "CHECK", `INSERT INTO docs VALUES ('tip', 'c', 'i', 'not json {', 0, 0)`)
	mustFail(t, db, "CHECK", `UPDATE users SET max_sites = -1`)

	mustExec(t, db, `INSERT INTO users(id, name, created_at) VALUES (2, 'amy', 0)`)
	mustExec(t, db, `INSERT INTO tokens(hash, user_id, label, created_at) VALUES ('h3', 2, 'laptop', 0)`) // another user may reuse a label
	mustExec(t, db, `INSERT INTO docs VALUES ('tip', '_kv', 'score', '42', 0, 0)`)                        // any JSON is a valid value
}

func TestSuperuserTokensBelongOnlyToSuperusers(t *testing.T) {
	db := schemaDB(t)
	mustExec(t, db, `INSERT INTO users(id, name, is_superuser, created_at) VALUES (1, 'root', 1, 0), (2, 'sam', 0, 0)`)

	mustExec(t, db, `INSERT INTO tokens(hash, user_id, label, is_superuser, created_at) VALUES ('r1', 1, 'admin', 1, 0)`)
	mustExec(t, db, `INSERT INTO tokens(hash, user_id, label, is_superuser, created_at) VALUES ('r2', 1, 'daily', 0, 0)`) // a superuser may hold ordinary tokens too
	mustExec(t, db, `INSERT INTO tokens(hash, user_id, label, is_superuser, created_at) VALUES ('s1', 2, 'laptop', 0, 0)`)

	const want = "superuser tokens can only belong to superusers"
	mustFail(t, db, want, `INSERT INTO tokens(hash, user_id, label, is_superuser, created_at) VALUES ('s2', 2, 'sneaky', 1, 0)`)
	mustFail(t, db, want, `UPDATE tokens SET is_superuser = 1 WHERE hash = 's1'`)
	mustFail(t, db, want, `UPDATE tokens SET user_id = 2 WHERE hash = 'r1'`) // moving an admin token to an ordinary user
}

func TestDemotingAUserDowngradesTheirTokens(t *testing.T) {
	db := schemaDB(t)
	mustExec(t, db, `INSERT INTO users(id, name, is_superuser, created_at) VALUES (1, 'root', 1, 0), (2, 'other', 1, 0)`)
	mustExec(t, db, `INSERT INTO tokens(hash, user_id, label, is_superuser, created_at) VALUES
		('a', 1, 'admin', 1, 0), ('b', 1, 'daily', 0, 0), ('c', 2, 'admin', 1, 0)`)

	mustExec(t, db, `UPDATE users SET is_superuser = 0 WHERE id = 1`)

	if n := count(t, db, `SELECT count(*) FROM tokens WHERE user_id = 1 AND is_superuser = 1`); n != 0 {
		t.Errorf("%d superuser tokens survived the demotion", n)
	}
	if n := count(t, db, `SELECT count(*) FROM tokens WHERE user_id = 1`); n != 2 {
		t.Errorf("the demoted user lost tokens: %d left, want 2 (downgraded, not deleted)", n)
	}
	if n := count(t, db, `SELECT count(*) FROM tokens WHERE user_id = 2 AND is_superuser = 1`); n != 1 {
		t.Error("another superuser's token was downgraded")
	}
}

func TestOpenDBBuildsAFreshDatabaseAndReopensIt(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	if version(t, db) != 1 || !hasTable(t, db, "docs") {
		t.Errorf("fresh database: version %d", version(t, db))
	}
	db.Close()
	db, err = openDB(dir) // a second start applies nothing and still works
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if version(t, db) != 1 {
		t.Errorf("reopened at version %d", version(t, db))
	}
}

func TestOpenDBRefusesTheOldDevelopmentSchema(t *testing.T) {
	dir := t.TempDir()
	old, err := openSQLite(dbPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, old, `CREATE TABLE tokens (hash TEXT PRIMARY KEY, owner TEXT NOT NULL)`)
	mustExec(t, old, `CREATE TABLE sites (name TEXT PRIMARY KEY, owner TEXT NOT NULL)`)
	old.Close()

	if _, err := openDB(dir); err == nil || !strings.Contains(err.Error(), "delete "+filepath.Join(dir, "housepage.db")) {
		t.Errorf("error %v, want a message telling you to delete the old database", err)
	}
}

func TestStatusReport(t *testing.T) {
	dir := t.TempDir()
	report, err := statusReport(dir)
	if err != nil || !strings.Contains(report, "no database yet") || !strings.Contains(report, "version 1") {
		t.Errorf("before the first start: %q %v", report, err)
	}
	if _, err := os.Stat(dbPath(dir)); err == nil {
		t.Fatal("reporting the status created a database file")
	}
	db, err := openDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	report, err = statusReport(dir)
	if err != nil || !strings.Contains(report, "version:  1 of 1") || !strings.Contains(report, "pending:  none") {
		t.Errorf("after the first start: %q %v", report, err)
	}
}
