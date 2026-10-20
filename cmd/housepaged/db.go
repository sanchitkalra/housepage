package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"time"

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

// ponytail: temporary layer. The handlers still treat an owner as a string, so these functions keep
// their old signatures on top of the new tables. Modules 3 to 5 replace them with user ids and delete this.

// ensureUser returns the id of the user called name, creating an ordinary user if there is none.
func ensureUser(db *sql.DB, name string) (int64, error) {
	if _, err := db.Exec(`INSERT INTO users(name, created_at) VALUES(?, ?) ON CONFLICT(name) DO NOTHING`, name, time.Now().UnixMilli()); err != nil {
		return 0, err
	}
	var id int64
	err := db.QueryRow(`SELECT id FROM users WHERE name = ?`, name).Scan(&id)
	return id, err
}

// createToken returns the plaintext token once; only its hash is stored.
func createToken(db *sql.DB, owner string) (string, error) {
	uid, err := ensureUser(db, owner)
	if err != nil {
		return "", err
	}
	return mintToken(db, uid, "t"+strconv.FormatInt(time.Now().UnixNano(), 36), false)
}

// ownerOfToken returns the user's name, or sql.ErrNoRows for an unknown token.
func ownerOfToken(db *sql.DB, token string) (string, error) {
	var owner string
	err := db.QueryRow(`SELECT u.name FROM tokens t JOIN users u ON u.id = t.user_id WHERE t.hash = ?`, hashToken(token)).Scan(&owner)
	return owner, err
}

// ownerOfSite returns sql.ErrNoRows for an unclaimed name.
func ownerOfSite(db *sql.DB, name string) (string, error) {
	var owner string
	err := db.QueryRow(`SELECT u.name FROM sites s JOIN users u ON u.id = s.owner_id WHERE s.name = ?`, name).Scan(&owner)
	return owner, err
}

func sitesOf(db *sql.DB, owner string) ([]string, error) {
	rows, err := db.Query(`SELECT s.name FROM sites s JOIN users u ON u.id = s.owner_id WHERE u.name = ? ORDER BY s.name`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

func release(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM sites WHERE name = ?`, name)
	return err
}

// claim registers name to owner if it is free, and returns whoever owns it afterwards.
func claim(db *sql.DB, name, owner string) (string, error) {
	uid, err := ensureUser(db, owner)
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(`INSERT INTO sites(name, owner_id) VALUES(?, ?) ON CONFLICT(name) DO NOTHING`, name, uid); err != nil {
		return "", err
	}
	return ownerOfSite(db, name)
}
