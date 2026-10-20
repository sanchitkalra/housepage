package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "housepage.db")
	db, err := openSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func fixtures(pairs ...string) fstest.MapFS {
	m := fstest.MapFS{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = &fstest.MapFile{Data: []byte(pairs[i+1])}
	}
	return m
}

func one() fstest.MapFS { return fixtures("0001_a.sql", "CREATE TABLE a(x INTEGER);") }

func three() fstest.MapFS {
	return fixtures(
		"0001_a.sql", "CREATE TABLE a(x INTEGER);",
		"0002_b.sql", "CREATE TABLE b(x INTEGER);",
		"0003_c.sql", "CREATE TABLE c(x INTEGER);",
	)
}

func version(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func foreignKeysOn(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var on int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatal(err)
	}
	return on == 1
}

func TestLoadMigrations(t *testing.T) {
	good := fixtures("0001_a.sql", "x", "0002_b.sql", "x", "README.md", "not a migration", "sub/0009_ignored.sql", "x")
	migs, err := loadMigrations(good)
	if err != nil || len(migs) != 2 || migs[0].name != "0001_a.sql" || migs[1].version != 2 {
		t.Fatalf("good set: %v %v", migs, err)
	}
	if migs, err := loadMigrations(fixtures()); err != nil || len(migs) != 0 {
		t.Errorf("empty set: %v %v", migs, err)
	}
	bad := map[string]struct {
		files fstest.MapFS
		want  string
	}{
		"gap":       {fixtures("0001_a.sql", "x", "0003_c.sql", "x"), "0002 is missing"},
		"no start":  {fixtures("0002_b.sql", "x"), "0001 is missing"},
		"duplicate": {fixtures("0001_a.sql", "x", "0002_b.sql", "x", "0002_other.sql", "x"), "two migrations are numbered 0002"},
		"bad name":  {fixtures("1_a.sql", "x"), "names look like"},
	}
	for name, c := range bad {
		if _, err := loadMigrations(c.files); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestFreshDatabaseAppliesEverythingOnceThenNothing(t *testing.T) {
	db, path := testDB(t)
	ctx := context.Background()
	applied, err := migrate(ctx, db, path, three())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"0001_a.sql", "0002_b.sql", "0003_c.sql"}; !reflect.DeepEqual(applied, want) {
		t.Errorf("applied %v, want %v", applied, want)
	}
	if version(t, db) != 3 || !hasTable(t, db, "a") || !hasTable(t, db, "c") {
		t.Errorf("version %d, tables missing", version(t, db))
	}
	if applied, err := migrate(ctx, db, path, three()); err != nil || len(applied) != 0 {
		t.Errorf("second start applied %v (err %v), want nothing", applied, err)
	}
	if baks, _ := filepath.Glob(path + ".before-*"); len(baks) != 0 {
		t.Errorf("backups made for a fresh database: %v", baks)
	}
}

func TestOnlyPendingMigrationsRun(t *testing.T) {
	db, path := testDB(t)
	ctx := context.Background()
	if _, err := migrate(ctx, db, path, one()); err != nil {
		t.Fatal(err)
	}
	applied, err := migrate(ctx, db, path, three())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"0002_b.sql", "0003_c.sql"}; !reflect.DeepEqual(applied, want) {
		t.Errorf("applied %v, want %v", applied, want)
	}
}

func TestFailedMigrationRollsBackCompletely(t *testing.T) {
	db, path := testDB(t)
	bad := fixtures(
		"0001_a.sql", "CREATE TABLE a(x INTEGER);",
		"0002_b.sql", "CREATE TABLE b(x INTEGER); THIS IS NOT SQL;",
	)
	applied, err := migrate(context.Background(), db, path, bad)
	if err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("error %v, want one naming 0002_b.sql", err)
	}
	if !reflect.DeepEqual(applied, []string{"0001_a.sql"}) {
		t.Errorf("applied %v", applied)
	}
	if version(t, db) != 1 || !hasTable(t, db, "a") {
		t.Errorf("version %d: the first migration should have stayed", version(t, db))
	}
	if hasTable(t, db, "b") {
		t.Error("table b from the failed migration survived")
	}
	if !foreignKeysOn(t, db) {
		t.Error("foreign keys were left off after a failure")
	}
}

func TestMigrationLeavingOrphanRowsIsRolledBack(t *testing.T) {
	db, path := testDB(t)
	files := fixtures(
		"0001_a.sql", "CREATE TABLE p(id INTEGER PRIMARY KEY); CREATE TABLE c(pid INTEGER REFERENCES p(id));",
		"0002_b.sql", "INSERT INTO c VALUES (99);",
	)
	_, err := migrate(context.Background(), db, path, files)
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("error %v, want a foreign key violation", err)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM c").Scan(&n)
	if version(t, db) != 1 || n != 0 {
		t.Errorf("version %d, rows in c %d: the orphan insert should be rolled back", version(t, db), n)
	}
}

func TestForeignKeysAreOnAfterMigrating(t *testing.T) {
	db, path := testDB(t)
	if !foreignKeysOn(t, db) {
		t.Fatal("foreign keys are off on a new connection")
	}
	if _, err := migrate(context.Background(), db, path, three()); err != nil {
		t.Fatal(err)
	}
	if !foreignKeysOn(t, db) {
		t.Error("foreign keys were left off after migrating")
	}
}

func TestRefusesADatabaseNewerThanTheBinary(t *testing.T) {
	db, path := testDB(t)
	ctx := context.Background()
	if _, err := migrate(ctx, db, path, three()); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate(ctx, db, path, one()); err == nil || !strings.Contains(err.Error(), "upgrade housepaged") {
		t.Errorf("error %v, want a request to upgrade", err)
	}
}

func TestRefusesAnOldDevelopmentDatabase(t *testing.T) {
	db, path := testDB(t)
	if _, err := db.Exec("CREATE TABLE tokens(hash TEXT)"); err != nil {
		t.Fatal(err)
	}
	_, err := migrate(context.Background(), db, path, three())
	if err == nil || !strings.Contains(err.Error(), "delete "+path) {
		t.Fatalf("error %v, want a message telling you to delete %s", err, path)
	}
	if version(t, db) != 0 || hasTable(t, db, "a") {
		t.Error("the old database was modified")
	}
}

func TestBackupIsTakenBeforeMigratingAnExistingDatabase(t *testing.T) {
	db, path := testDB(t)
	ctx := context.Background()
	if _, err := migrate(ctx, db, path, one()); err != nil {
		t.Fatal(err)
	}
	if baks, _ := filepath.Glob(path + ".before-*"); len(baks) != 0 {
		t.Fatalf("a brand-new database should not be backed up: %v", baks)
	}
	if _, err := migrate(ctx, db, path, three()); err != nil {
		t.Fatal(err)
	}
	bak := path + ".before-0002.bak"
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("no backup at %s: %v", bak, err)
	}
	old, err := openSQLite(bak)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if version(t, old) != 1 || !hasTable(t, old, "a") || hasTable(t, old, "b") {
		t.Errorf("the backup should hold the database as it was at version 1 (version %d)", version(t, old))
	}
}

func TestTwoProcessesStartingAtOnceApplyEachMigrationOnce(t *testing.T) {
	_, path := testDB(t)
	files := fixtures(
		"0001_a.sql", "CREATE TABLE once(n INTEGER); INSERT INTO once VALUES (1);",
		"0002_b.sql", "INSERT INTO once VALUES (2);",
	)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := openSQLite(path)
			if err != nil {
				errs[i] = err
				return
			}
			defer db.Close()
			_, errs[i] = migrate(context.Background(), db, path, files)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("process %d: %v", i, err)
		}
	}
	db, _ := openSQLite(path)
	defer db.Close()
	var rows int
	db.QueryRow("SELECT count(*) FROM once").Scan(&rows)
	if rows != 2 || version(t, db) != 2 {
		t.Errorf("rows %d, version %d: each migration should have run exactly once", rows, version(t, db))
	}
}

func TestStatusReportsWithoutChangingAnything(t *testing.T) {
	db, path := testDB(t)
	ctx := context.Background()
	st, err := migrateStatus(ctx, db, path, three())
	if err != nil || st.current != 0 || st.latest != 3 || len(st.pending) != 3 {
		t.Fatalf("fresh: %+v %v", st, err)
	}
	if hasTable(t, db, "a") {
		t.Error("status applied a migration")
	}
	if _, err := migrate(ctx, db, path, one()); err != nil {
		t.Fatal(err)
	}
	st, err = migrateStatus(ctx, db, path, three())
	if err != nil || st.current != 1 || !reflect.DeepEqual(st.pending, []string{"0002_b.sql", "0003_c.sql"}) {
		t.Errorf("after one: %+v %v", st, err)
	}
	if _, err := migrateStatus(ctx, db, path, fixtures()); err == nil {
		t.Error("status should refuse a database newer than the binary")
	}
}

func TestOpenSQLiteSetsThePragmasEveryConnectionNeeds(t *testing.T) {
	db, _ := testDB(t)
	var timeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		t.Errorf("busy_timeout %d (%v), want 5000", timeout, err)
	}
	if !foreignKeysOn(t, db) {
		t.Error("foreign_keys is off")
	}
}
