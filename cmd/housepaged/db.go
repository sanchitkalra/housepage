package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// openSQLite opens the database file with the settings every connection needs. They are in the
// connection string, not run once, because foreign_keys belongs to a connection and would be lost
// whenever the pool opens a new one.
func openSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func dbPath(dataDir string) string { return filepath.Join(dataDir, "housepage.db") }

// openDB opens the database in dataDir and brings its schema up to date.
func openDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := dbPath(dataDir)
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	if _, err := migrate(context.Background(), db, path, embeddedMigrations()); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
