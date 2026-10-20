package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ponytail: an owner is just a label on a token; add a users table when accounts exist.
const schema = `
CREATE TABLE IF NOT EXISTS tokens (hash TEXT PRIMARY KEY, owner TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sites  (name TEXT PRIMARY KEY, owner TEXT NOT NULL);`

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

func openDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	db, err := openSQLite(filepath.Join(dataDir, "housepage.db"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// createToken returns the plaintext token once; only its hash is stored.
func createToken(db *sql.DB, owner string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := "hp_" + hex.EncodeToString(b)
	_, err := db.Exec(`INSERT INTO tokens(hash, owner) VALUES(?, ?)`, hashToken(token), owner)
	return token, err
}

// ownerOfToken returns sql.ErrNoRows for an unknown token.
func ownerOfToken(db *sql.DB, token string) (string, error) {
	var owner string
	err := db.QueryRow(`SELECT owner FROM tokens WHERE hash = ?`, hashToken(token)).Scan(&owner)
	return owner, err
}

// ownerOfSite returns sql.ErrNoRows for an unclaimed name.
func ownerOfSite(db *sql.DB, name string) (string, error) {
	var owner string
	err := db.QueryRow(`SELECT owner FROM sites WHERE name = ?`, name).Scan(&owner)
	return owner, err
}

func sitesOf(db *sql.DB, owner string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sites WHERE owner = ? ORDER BY name`, owner)
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
	if _, err := db.Exec(`INSERT INTO sites(name, owner) VALUES(?, ?) ON CONFLICT(name) DO NOTHING`, name, owner); err != nil {
		return "", err
	}
	var current string
	err := db.QueryRow(`SELECT owner FROM sites WHERE name = ?`, name).Scan(&current)
	return current, err
}
