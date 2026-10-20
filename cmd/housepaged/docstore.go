package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"housepage/internal/rules"
)

var (
	errNotObject = errors.New("a document must be a JSON object")
	errTooLarge  = errors.New("document is over the size limit")
)

// quotaError says a write would go over one of the site's limits. The API reports it as quota_exceeded.
type quotaError struct {
	what       string // "collections", "documents" or "entries"
	limit      int
	collection string // the collection the write was for
}

func (e *quotaError) Error() string {
	switch e.what {
	case "collections":
		return fmt.Sprintf("site is at its limit of %d collections; %q would be a new one", e.limit, e.collection)
	case "documents":
		return fmt.Sprintf("collection %q is full: the limit is %d documents", e.collection, e.limit)
	}
	return fmt.Sprintf("the key-value store is full: the limit is %d entries", e.limit)
}

// tooLargeError is errTooLarge with the sizes, for the message.
type tooLargeError struct {
	what        string // "document" or "value"
	size, limit int
}

func (e *tooLargeError) Error() string {
	return fmt.Sprintf("%s is %d bytes; the limit is %d", e.what, e.size, e.limit)
}

func (e *tooLargeError) Is(target error) bool { return target == errTooLarge }

// doc is a stored document as the API shows it: the envelope around the caller's data.
type doc struct {
	ID        string
	CreatedAt int64
	UpdatedAt int64
	Data      json.RawMessage
}

// siteLimits are a site's data settings with the defaults filled in.
type siteLimits struct {
	Mode           string
	MaxCollections int
	MaxDocs        int
	MaxDocSize     int
}

// dbQuerier is what both *sql.DB and *sql.Tx offer, so a helper can run inside or outside a transaction.
type dbQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func defaultNowMs() int64 { return time.Now().UnixMilli() }

var nowMs = defaultNowMs

// getSiteLimits reads a site's settings. No row means mode off and every default.
func getSiteLimits(q dbQuerier, site string) (siteLimits, error) {
	l := siteLimits{Mode: "off", MaxCollections: rules.DefaultMaxCollections, MaxDocs: rules.DefaultMaxDocs, MaxDocSize: rules.DefaultMaxDocSize}
	var mode string
	var mc, md, ms sql.NullInt64
	err := q.QueryRow(`SELECT mode, max_collections, max_docs, max_doc_size FROM site_db_config WHERE site = ?`, site).Scan(&mode, &mc, &md, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return siteLimits{}, err
	}
	l.Mode = mode
	if mc.Valid {
		l.MaxCollections = int(mc.Int64)
	}
	if md.Valid {
		l.MaxDocs = int(md.Int64)
	}
	if ms.Valid {
		l.MaxDocSize = int(ms.Int64)
	}
	return l, nil
}

// prepare compacts the JSON and checks it against the size limit (and, for a collection, that it is an object).
// The size that counts is the stored text.
func prepare(data json.RawMessage, object bool, maxSize int) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return "", err
	}
	if object && buf.Bytes()[0] != '{' {
		return "", errNotObject
	}
	if buf.Len() > maxSize {
		what := "value"
		if object {
			what = "document"
		}
		return "", &tooLargeError{what, buf.Len(), maxSize}
	}
	return buf.String(), nil
}

func countDocs(tx *sql.Tx, site, collection string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM docs WHERE site = ? AND collection = ?`, site, collection).Scan(&n)
	return n, err
}

// countCollections counts the site's collections by hopping from one name to the next, which touches one
// row per collection instead of every document. _kv is not a collection.
func countCollections(tx *sql.Tx, site string) (int, error) {
	var n int
	err := tx.QueryRow(`WITH RECURSIVE c(name) AS (
  SELECT min(collection) FROM docs WHERE site = ? AND collection <> '_kv'
  UNION ALL
  SELECT (SELECT min(collection) FROM docs WHERE site = ? AND collection > c.name AND collection <> '_kv')
  FROM c WHERE c.name IS NOT NULL
) SELECT count(*) FROM c WHERE name IS NOT NULL`, site, site).Scan(&n)
	return n, err
}

// insertDoc adds a document with a new id to a collection. A collection with no documents does not exist, so
// the first insert needs a free collection slot. The checks and the insert are one transaction.
func insertDoc(db *sql.DB, site, collection string, data json.RawMessage) (doc, error) {
	tx, err := db.Begin()
	if err != nil {
		return doc{}, err
	}
	defer tx.Rollback()

	lim, err := getSiteLimits(tx, site)
	if err != nil {
		return doc{}, err
	}
	text, err := prepare(data, true, lim.MaxDocSize)
	if err != nil {
		return doc{}, err
	}
	n, err := countDocs(tx, site, collection)
	if err != nil {
		return doc{}, err
	}
	if n == 0 {
		used, err := countCollections(tx, site)
		if err != nil {
			return doc{}, err
		}
		if used >= lim.MaxCollections {
			return doc{}, &quotaError{what: "collections", limit: lim.MaxCollections, collection: collection}
		}
	}
	if n >= lim.MaxDocs {
		return doc{}, &quotaError{what: "documents", limit: lim.MaxDocs, collection: collection}
	}
	id, err := newID()
	if err != nil {
		return doc{}, err
	}
	now := nowMs()
	if _, err := tx.Exec(`INSERT INTO docs(site, collection, id, value, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)`,
		site, collection, id, text, now, now); err != nil {
		return doc{}, err
	}
	if err := tx.Commit(); err != nil {
		return doc{}, err
	}
	return doc{ID: id, CreatedAt: now, UpdatedAt: now, Data: json.RawMessage(text)}, nil
}

// replaceDoc swaps a document's data. There is no upsert: a missing id is errNotFound. It keeps created_at
// and is allowed at any cap, since it adds nothing.
func replaceDoc(db *sql.DB, site, collection, id string, data json.RawMessage) (doc, error) {
	tx, err := db.Begin()
	if err != nil {
		return doc{}, err
	}
	defer tx.Rollback()

	lim, err := getSiteLimits(tx, site)
	if err != nil {
		return doc{}, err
	}
	text, err := prepare(data, true, lim.MaxDocSize)
	if err != nil {
		return doc{}, err
	}
	now := nowMs()
	var created int64
	err = tx.QueryRow(`UPDATE docs SET value = ?, updated_at = ? WHERE site = ? AND collection = ? AND id = ? RETURNING created_at`,
		text, now, site, collection, id).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return doc{}, errNotFound
	}
	if err != nil {
		return doc{}, err
	}
	if err := tx.Commit(); err != nil {
		return doc{}, err
	}
	return doc{ID: id, CreatedAt: created, UpdatedAt: now, Data: json.RawMessage(text)}, nil
}

// putKV stores any JSON value under a key. created is false when it replaced an existing entry, which is
// allowed at any cap. _kv has its own entry cap, the same number as maxDocs.
func putKV(db *sql.DB, site, key string, data json.RawMessage) (d doc, created bool, err error) {
	tx, err := db.Begin()
	if err != nil {
		return doc{}, false, err
	}
	defer tx.Rollback()

	lim, err := getSiteLimits(tx, site)
	if err != nil {
		return doc{}, false, err
	}
	text, err := prepare(data, false, lim.MaxDocSize)
	if err != nil {
		return doc{}, false, err
	}
	now := nowMs()
	var made int64
	err = tx.QueryRow(`UPDATE docs SET value = ?, updated_at = ? WHERE site = ? AND collection = ? AND id = ? RETURNING created_at`,
		text, now, site, rules.KVCollection, key).Scan(&made)
	switch {
	case err == nil:
		created = false
	case errors.Is(err, sql.ErrNoRows):
		n, err := countDocs(tx, site, rules.KVCollection)
		if err != nil {
			return doc{}, false, err
		}
		if n >= lim.MaxDocs {
			return doc{}, false, &quotaError{what: "entries", limit: lim.MaxDocs}
		}
		if _, err := tx.Exec(`INSERT INTO docs(site, collection, id, value, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)`,
			site, rules.KVCollection, key, text, now, now); err != nil {
			return doc{}, false, err
		}
		made, created = now, true
	default:
		return doc{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return doc{}, false, err
	}
	return doc{ID: key, CreatedAt: made, UpdatedAt: now, Data: json.RawMessage(text)}, created, nil
}

// getDoc returns one document, or errNotFound. KV entries are documents of the _kv collection.
func getDoc(db *sql.DB, site, collection, id string) (doc, error) {
	d := doc{ID: id}
	var text string
	err := db.QueryRow(`SELECT value, created_at, updated_at FROM docs WHERE site = ? AND collection = ? AND id = ?`,
		site, collection, id).Scan(&text, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return doc{}, errNotFound
	}
	d.Data = json.RawMessage(text)
	return d, err
}

// deleteDoc removes a document. A missing one is not an error. Removing a collection's last document frees its slot.
func deleteDoc(db *sql.DB, site, collection, id string) error {
	_, err := db.Exec(`DELETE FROM docs WHERE site = ? AND collection = ? AND id = ?`, site, collection, id)
	return err
}
