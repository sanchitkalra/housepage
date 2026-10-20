package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type migration struct {
	version int
	name    string
	sql     string
}

type migrationStatus struct {
	current int
	latest  int
	pending []string
}

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// loadMigrations reads the numbered .sql files at the root of fsys and checks they run 1, 2, 3 ...
// with no gap and no duplicate. Other files are ignored.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var migs []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q: names look like 0001_what_it_does.sql", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		migs = append(migs, migration{version: version, name: e.Name(), sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for i, m := range migs {
		switch {
		case m.version == i+1:
		case i > 0 && m.version == migs[i-1].version:
			return nil, fmt.Errorf("two migrations are numbered %04d: %s and %s", m.version, migs[i-1].name, m.name)
		default:
			return nil, fmt.Errorf("migration %04d is missing: %s comes after %d migration(s)", i+1, m.name, i)
		}
	}
	return migs, nil
}

func userVersion(ctx context.Context, conn *sql.Conn) (int, error) {
	var v int
	err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

func hasTables(ctx context.Context, conn *sql.Conn) (bool, error) {
	var n int
	err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&n)
	return n > 0, err
}

func newerDatabase(current, latest int) error {
	return fmt.Errorf("the database is at version %d but this housepaged only knows versions up to %d; upgrade housepaged", current, latest)
}

func developmentDatabase(path string) error {
	return fmt.Errorf("this database was created by an earlier development version and cannot be upgraded; delete %s and start again", path)
}

// migrateStatus reports where the database stands without changing anything.
func migrateStatus(ctx context.Context, db *sql.DB, dbPath string, fsys fs.FS) (migrationStatus, error) {
	var st migrationStatus
	migs, err := loadMigrations(fsys)
	if err != nil {
		return st, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return st, err
	}
	defer conn.Close()
	if st.current, err = userVersion(ctx, conn); err != nil {
		return st, err
	}
	if len(migs) > 0 {
		st.latest = migs[len(migs)-1].version
	}
	if st.current > st.latest {
		return st, newerDatabase(st.current, st.latest)
	}
	if st.current == 0 {
		has, err := hasTables(ctx, conn)
		if err != nil {
			return st, err
		}
		if has {
			return st, developmentDatabase(dbPath)
		}
	}
	for _, m := range migs {
		if m.version > st.current {
			st.pending = append(st.pending, m.name)
		}
	}
	return st, nil
}

// migrate applies every migration numbered above the database's version, once each and in order.
// It returns the names of the files it applied.
func migrate(ctx context.Context, db *sql.DB, dbPath string, fsys fs.FS) ([]string, error) {
	migs, err := loadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	// one pinned connection: foreign_keys and the transactions below belong to a connection
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	current, err := userVersion(ctx, conn)
	if err != nil {
		return nil, err
	}
	latest := 0
	if len(migs) > 0 {
		latest = migs[len(migs)-1].version
	}
	if current > latest {
		return nil, newerDatabase(current, latest)
	}
	var pending []migration
	for _, m := range migs {
		if m.version > current {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}
	if current >= 1 {
		if err := backup(ctx, conn, dbPath, pending[0].version); err != nil {
			return nil, err
		}
	}

	// off before the first transaction (the pragma is ignored inside one), back on however we leave
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return nil, err
	}
	defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")

	var applied []string
	for _, m := range pending {
		ok, err := applyOne(ctx, conn, m, dbPath)
		if err != nil {
			return applied, err
		}
		if ok {
			applied = append(applied, m.name)
			log.Printf("migration-applied | version=%d | file=%s", m.version, m.name)
		}
	}
	return applied, nil
}

// applyOne runs one migration in one transaction and records its version inside that transaction.
// It reports false when another process applied the migration first.
func applyOne(ctx context.Context, conn *sql.Conn, m migration, dbPath string) (bool, error) {
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil { // takes the write lock: a second process waits here
		return false, err
	}
	finished := false
	defer func() {
		if !finished {
			conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	v, err := userVersion(ctx, conn)
	if err != nil {
		return false, err
	}
	if v >= m.version {
		return false, nil
	}
	if v == 0 {
		has, err := hasTables(ctx, conn)
		if err != nil {
			return false, err
		}
		if has {
			return false, developmentDatabase(dbPath)
		}
	}
	if _, err := conn.ExecContext(ctx, m.sql); err != nil {
		return false, fmt.Errorf("migration %s failed: %w", m.name, err)
	}
	if err := foreignKeyCheck(ctx, conn); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return false, err
	}
	finished = true
	return true, nil
}

// foreignKeyCheck fails if any row now refers to a row that does not exist.
func foreignKeyCheck(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	var bad []string
	for rows.Next() && len(bad) < 5 {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		bad = append(bad, fmt.Sprintf("a row in %s refers to a missing row in %s", table, parent))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("foreign key violations after the migration: %s", strings.Join(bad, "; "))
	}
	return nil
}

// backup writes a consistent copy of the database next to it, named after the first pending version.
func backup(ctx context.Context, conn *sql.Conn, dbPath string, firstPending int) error {
	dst := fmt.Sprintf("%s.before-%04d.bak", dbPath, firstPending)
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := conn.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(dst, "'", "''")+"'"); err != nil {
		return fmt.Errorf("could not back up the database before migrating: %w", err)
	}
	log.Printf("migration-backup | file=%s", dst)
	return nil
}
