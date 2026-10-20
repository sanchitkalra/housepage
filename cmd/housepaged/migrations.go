package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// embeddedMigrations is the set compiled into the binary, with the files at the root.
func embeddedMigrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err) // the directory is embedded above, so this is a build mistake
	}
	return sub
}

// statusReport describes where the database in dataDir stands. It never creates or changes anything.
func statusReport(dataDir string) (string, error) {
	path := dbPath(dataDir)
	migs, err := loadMigrations(embeddedMigrations())
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("no database yet at %s; the first start creates it at version %d\n", path, len(migs)), nil
	}
	db, err := openSQLite(path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	st, err := migrateStatus(context.Background(), db, path, embeddedMigrations())
	if err != nil {
		return "", err
	}
	pending := "none"
	if len(st.pending) > 0 {
		pending = strings.Join(st.pending, ", ")
	}
	return fmt.Sprintf("database: %s\nversion:  %d of %d\npending:  %s\n", path, st.current, st.latest, pending), nil
}
