package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"housepage/internal/rules"
)

var (
	errUserExists   = errors.New("a user with that name already exists")
	errLabelExists  = errors.New("this user already has a token with that label")
	errNotSuperuser = errors.New("superuser tokens can only belong to superusers")
	errUnknownToken = errors.New("unknown token")
	errNotFound     = errors.New("not found")
)

type user struct {
	ID          int64
	Name        string
	IsSuperuser bool
	MaxSites    *int // nil means the default
	CreatedAt   int64
}

// siteLimit is the number of sites the user may own.
func (u user) siteLimit() int {
	if u.MaxSites != nil {
		return *u.MaxSites
	}
	return rules.DefaultMaxSites
}

// tokenInfo is what listing a user's tokens shows: never the hash or the plaintext.
type tokenInfo struct {
	Label       string
	IsSuperuser bool
	CreatedAt   int64
}

// caller is who a valid token belongs to.
type caller struct {
	UserID      int64
	UserName    string
	TokenLabel  string
	IsSuperuser bool // this token is a superuser token
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// newToken is a variable so a test can force a failure after the user row has been written.
var newToken = func() (plaintext, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	plaintext = "hp_" + hex.EncodeToString(b)
	return plaintext, hashToken(plaintext), nil
}

// insertToken stores a new token and returns its plaintext, which is never stored.
func insertToken(db execer, userID int64, label string, superuser bool, now int64) (string, error) {
	plaintext, hash, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = db.Exec(`INSERT INTO tokens(hash, user_id, label, is_superuser, created_at) VALUES(?, ?, ?, ?, ?)`,
		hash, userID, label, boolInt(superuser), now)
	switch {
	case err == nil:
		return plaintext, nil
	case strings.Contains(err.Error(), "UNIQUE constraint failed: tokens.user_id, tokens.label"):
		return "", errLabelExists
	case strings.Contains(err.Error(), errNotSuperuser.Error()):
		return "", errNotSuperuser
	}
	return "", err
}

// createUser adds a user and, in the same transaction, their first token, labelled "default".
// The default token has the user's level: a superuser gets a superuser token.
func createUser(db *sql.DB, name string, superuser bool) (user, string, error) {
	if err := rules.ValidUserName(name); err != nil {
		return user{}, "", err
	}
	tx, err := db.Begin()
	if err != nil {
		return user{}, "", err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	res, err := tx.Exec(`INSERT INTO users(name, is_superuser, created_at) VALUES(?, ?, ?)`, name, boolInt(superuser), now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.name") {
			return user{}, "", errUserExists
		}
		return user{}, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return user{}, "", err
	}
	token, err := insertToken(tx, id, "default", superuser, now)
	if err != nil {
		return user{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return user{}, "", err
	}
	return user{ID: id, Name: name, IsSuperuser: superuser, CreatedAt: now}, token, nil
}

// mintToken adds a labelled token for a user and returns its plaintext once. Whether the caller
// may do this is decided by the API layer; the database only refuses a superuser token for an ordinary user.
func mintToken(db *sql.DB, userID int64, label string, superuser bool) (string, error) {
	if err := rules.ValidLabel(label); err != nil {
		return "", err
	}
	return insertToken(db, userID, label, superuser, time.Now().UnixMilli())
}

// lookupToken finds who a token belongs to, or returns errUnknownToken.
func lookupToken(db *sql.DB, token string) (caller, error) {
	var c caller
	var super int
	err := db.QueryRow(`SELECT u.id, u.name, t.label, t.is_superuser FROM tokens t JOIN users u ON u.id = t.user_id WHERE t.hash = ?`,
		hashToken(token)).Scan(&c.UserID, &c.UserName, &c.TokenLabel, &super)
	if errors.Is(err, sql.ErrNoRows) {
		return caller{}, errUnknownToken
	}
	c.IsSuperuser = super == 1
	return c, err
}

// listTokens returns a user's tokens, oldest first.
func listTokens(db *sql.DB, userID int64) ([]tokenInfo, error) {
	rows, err := db.Query(`SELECT label, is_superuser, created_at FROM tokens WHERE user_id = ? ORDER BY created_at, label`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tokenInfo{}
	for rows.Next() {
		var t tokenInfo
		var super int
		if err := rows.Scan(&t.Label, &super, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.IsSuperuser = super == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// revokeToken deletes one of a user's tokens. Another user's label is errNotFound, like a missing one.
func revokeToken(db *sql.DB, userID int64, label string) error {
	res, err := db.Exec(`DELETE FROM tokens WHERE user_id = ? AND label = ?`, userID, label)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func getUserByName(db *sql.DB, name string) (user, error) {
	var u user
	var super int
	var max sql.NullInt64
	err := db.QueryRow(`SELECT id, name, is_superuser, max_sites, created_at FROM users WHERE name = ?`, name).
		Scan(&u.ID, &u.Name, &super, &max, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return user{}, errNotFound
	}
	if err != nil {
		return user{}, err
	}
	u.IsSuperuser = super == 1
	if max.Valid {
		n := int(max.Int64)
		u.MaxSites = &n
	}
	return u, nil
}

// setMaxSites changes a user's site limit; nil restores the default.
func setMaxSites(db *sql.DB, userID int64, n *int) error {
	var v any
	if n != nil {
		v = *n
	}
	res, err := db.Exec(`UPDATE users SET max_sites = ? WHERE id = ?`, v, userID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return errNotFound
	}
	return nil
}

// setSuperuser promotes or demotes a user. Demoting downgrades their superuser tokens (a trigger does it);
// promoting does not upgrade any existing token.
func setSuperuser(db *sql.DB, userID int64, superuser bool) error {
	res, err := db.Exec(`UPDATE users SET is_superuser = ? WHERE id = ?`, boolInt(superuser), userID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return errNotFound
	}
	return nil
}
