package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// docsDB is a migrated database with one user and a site "s" that has no settings row.
func docsDB(t *testing.T) *sql.DB {
	t.Helper()
	db := schemaDB(t)
	mustExec(t, db, `INSERT INTO users(id, name, created_at) VALUES(1, 'bob', 0)`)
	mustExec(t, db, `INSERT INTO sites(name, owner_id) VALUES('s', 1)`)
	return db
}

func setLimits(t *testing.T, db *sql.DB, mc, md, ms any) {
	t.Helper()
	mustExec(t, db, `INSERT INTO site_db_config(site, mode, max_collections, max_docs, max_doc_size) VALUES('s', 'open', ?, ?, ?)
		ON CONFLICT(site) DO UPDATE SET max_collections = excluded.max_collections, max_docs = excluded.max_docs, max_doc_size = excluded.max_doc_size`,
		mc, md, ms)
}

func mustInsert(t *testing.T, db *sql.DB, collection, body string) doc {
	t.Helper()
	d, err := insertDoc(db, "s", collection, json.RawMessage(body))
	if err != nil {
		t.Fatalf("insert into %s: %v", collection, err)
	}
	return d
}

func isQuota(err error, what string) bool {
	var q *quotaError
	return errors.As(err, &q) && q.what == what
}

func TestSiteLimitsDefaultsAndOverrides(t *testing.T) {
	db := docsDB(t)
	got, err := getSiteLimits(db, "s")
	if err != nil || got != (siteLimits{"off", 5, 500, 4096}) {
		t.Fatalf("no row: %+v, %v", got, err)
	}
	mustExec(t, db, `INSERT INTO site_db_config(site, mode, max_docs) VALUES('s', 'owner', 7)`)
	got, err = getSiteLimits(db, "s")
	if err != nil || got != (siteLimits{"owner", 5, 7, 4096}) {
		t.Fatalf("partial row (NULL means default): %+v, %v", got, err)
	}
}

func TestInsertGetReplaceDelete(t *testing.T) {
	db := docsDB(t)
	d := mustInsert(t, db, "events", `{ "title": "Launch" }`)
	if len(d.ID) != 26 || string(d.Data) != `{"title":"Launch"}` || d.CreatedAt != d.UpdatedAt {
		t.Fatalf("insert returned %+v", d)
	}
	got, err := getDoc(db, "s", "events", d.ID)
	if err != nil || got.ID != d.ID || string(got.Data) != `{"title":"Launch"}` || got.CreatedAt != d.CreatedAt {
		t.Fatalf("get: %+v, %v", got, err)
	}

	nowMs = func() int64 { return d.CreatedAt + 1000 }
	t.Cleanup(func() { nowMs = defaultNowMs })
	r, err := replaceDoc(db, "s", "events", d.ID, json.RawMessage(`{"title":"Kickoff"}`))
	if err != nil || r.CreatedAt != d.CreatedAt || r.UpdatedAt != d.CreatedAt+1000 || string(r.Data) != `{"title":"Kickoff"}` {
		t.Fatalf("replace: %+v, %v", r, err)
	}
	if got, _ := getDoc(db, "s", "events", d.ID); string(got.Data) != `{"title":"Kickoff"}` {
		t.Errorf("replace was not stored: %s", got.Data)
	}

	if err := deleteDoc(db, "s", "events", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := getDoc(db, "s", "events", d.ID); !errors.Is(err, errNotFound) {
		t.Errorf("get after delete: %v, want errNotFound", err)
	}
	if err := deleteDoc(db, "s", "events", d.ID); err != nil {
		t.Errorf("deleting twice must be fine, got %v", err)
	}
}

func TestReplaceNeverCreates(t *testing.T) {
	db := docsDB(t)
	mustInsert(t, db, "events", `{}`)
	if _, err := replaceDoc(db, "s", "events", "NOPE", json.RawMessage(`{}`)); !errors.Is(err, errNotFound) {
		t.Errorf("replace of a missing id: %v, want errNotFound", err)
	}
	if _, err := replaceDoc(db, "s", "other", "NOPE", json.RawMessage(`{}`)); !errors.Is(err, errNotFound) {
		t.Errorf("replace in a missing collection: %v, want errNotFound", err)
	}
	if n := count(t, db, `SELECT count(*) FROM docs`); n != 1 {
		t.Errorf("%d documents, want 1", n)
	}
}

func TestCollectionDocumentsMustBeObjects(t *testing.T) {
	db := docsDB(t)
	for _, body := range []string{`42`, `"x"`, `[1]`, `null`, `true`} {
		if _, err := insertDoc(db, "s", "c", json.RawMessage(body)); !errors.Is(err, errNotObject) {
			t.Errorf("insert %s: %v, want errNotObject", body, err)
		}
	}
	d := mustInsert(t, db, "c", `{}`)
	if _, err := replaceDoc(db, "s", "c", d.ID, json.RawMessage(`[1]`)); !errors.Is(err, errNotObject) {
		t.Errorf("replace with an array: %v, want errNotObject", err)
	}
	if _, err := insertDoc(db, "s", "c", json.RawMessage(`{bad`)); err == nil || errors.Is(err, errNotObject) {
		t.Errorf("invalid JSON: %v, want a JSON error", err)
	}
}

func TestDocumentSizeIsTheStoredText(t *testing.T) {
	db := docsDB(t)
	pad := func(n int) string { return `{"a":"` + strings.Repeat("x", n-8) + `"}` } // n bytes in all
	mustInsert(t, db, "c", pad(4096))
	if _, err := insertDoc(db, "s", "c", json.RawMessage(pad(4097))); !errors.Is(err, errTooLarge) {
		t.Errorf("4097 bytes: %v, want errTooLarge", err)
	}
	// whitespace the caller sent does not count: only the stored text does
	mustInsert(t, db, "c", `{ "a" : "`+strings.Repeat("x", 4096-8)+`" }`)

	setLimits(t, db, nil, nil, 10)
	if _, err := insertDoc(db, "s", "c", json.RawMessage(`{"a":"xxxxxx"}`)); !errors.Is(err, errTooLarge) {
		t.Errorf("14 bytes at limit 10: %v, want errTooLarge", err)
	}
	mustInsert(t, db, "c", `{"a":"xx"}`) // 10 bytes
	d := mustInsert(t, db, "c", `{}`)
	if _, err := replaceDoc(db, "s", "c", d.ID, json.RawMessage(`{"a":"xxxxxx"}`)); !errors.Is(err, errTooLarge) {
		t.Errorf("replace over the limit: %v, want errTooLarge", err)
	}
	if _, _, err := putKV(db, "s", "k", json.RawMessage(`"xxxxxxxxxxx"`)); !errors.Is(err, errTooLarge) {
		t.Errorf("kv over the limit: %v, want errTooLarge", err)
	}
}

func TestCollectionCap(t *testing.T) {
	db := docsDB(t)
	setLimits(t, db, 2, nil, nil)
	mustInsert(t, db, "a", `{}`)
	mustInsert(t, db, "b", `{}`)
	if _, err := insertDoc(db, "s", "c", json.RawMessage(`{}`)); !isQuota(err, "collections") {
		t.Fatalf("third collection: %v, want a collections quota error", err)
	}
	mustInsert(t, db, "a", `{}`) // an existing collection still takes documents
	if n := count(t, db, `SELECT count(*) FROM docs WHERE collection = 'c'`); n != 0 {
		t.Errorf("the refused insert left %d documents", n)
	}
}

func TestKVIsNotACollection(t *testing.T) {
	db := docsDB(t)
	setLimits(t, db, 1, nil, nil)
	if _, created, err := putKV(db, "s", "score", json.RawMessage(`42`)); err != nil || !created {
		t.Fatalf("kv put: created=%v, %v", created, err)
	}
	mustInsert(t, db, "a", `{}`) // the one collection slot is still free
	if _, err := insertDoc(db, "s", "b", json.RawMessage(`{}`)); !isQuota(err, "collections") {
		t.Errorf("second collection: %v, want a quota error", err)
	}
}

func TestDocumentCapAndReplaceAtCap(t *testing.T) {
	db := docsDB(t)
	setLimits(t, db, nil, 2, nil)
	a := mustInsert(t, db, "c", `{}`)
	mustInsert(t, db, "c", `{}`)
	if _, err := insertDoc(db, "s", "c", json.RawMessage(`{}`)); !isQuota(err, "documents") {
		t.Fatalf("third document: %v, want a documents quota error", err)
	}
	if _, err := replaceDoc(db, "s", "c", a.ID, json.RawMessage(`{"n":1}`)); err != nil {
		t.Errorf("replace at the cap must work: %v", err)
	}
	mustInsert(t, db, "d", `{}`) // the cap is per collection
	if err := deleteDoc(db, "s", "c", a.ID); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, db, "c", `{}`) // a slot is free again
}

func TestKVEntryCap(t *testing.T) {
	db := docsDB(t)
	setLimits(t, db, nil, 2, nil)
	for _, k := range []string{"a", "b"} {
		if _, created, err := putKV(db, "s", k, json.RawMessage(`1`)); err != nil || !created {
			t.Fatalf("put %s: created=%v, %v", k, created, err)
		}
	}
	if _, _, err := putKV(db, "s", "c", json.RawMessage(`1`)); !isQuota(err, "entries") {
		t.Fatalf("third entry: %v, want an entries quota error", err)
	}
	d, created, err := putKV(db, "s", "a", json.RawMessage(`{"any":["json"]}`))
	if err != nil || created || string(d.Data) != `{"any":["json"]}` {
		t.Errorf("replace at the cap: %+v created=%v, %v", d, created, err)
	}
	mustInsert(t, db, "c", `{}`) // documents in collections do not use the entry cap
}

func TestKVPutKeepsCreatedAt(t *testing.T) {
	db := docsDB(t)
	nowMs = func() int64 { return 1000 }
	t.Cleanup(func() { nowMs = defaultNowMs })
	first, created, err := putKV(db, "s", "k", json.RawMessage(`1`))
	if err != nil || !created || first.CreatedAt != 1000 {
		t.Fatalf("first put: %+v created=%v, %v", first, created, err)
	}
	nowMs = func() int64 { return 2000 }
	second, created, err := putKV(db, "s", "k", json.RawMessage(`2`))
	if err != nil || created || second.CreatedAt != 1000 || second.UpdatedAt != 2000 {
		t.Errorf("second put: %+v created=%v, %v", second, created, err)
	}
	got, err := getDoc(db, "s", "_kv", "k")
	if err != nil || string(got.Data) != `2` || got.CreatedAt != 1000 {
		t.Errorf("get: %+v, %v", got, err)
	}
}

func TestEmptyingACollectionFreesItsSlot(t *testing.T) {
	db := docsDB(t)
	setLimits(t, db, 1, nil, nil)
	a := mustInsert(t, db, "a", `{}`)
	if _, err := insertDoc(db, "s", "b", json.RawMessage(`{}`)); !isQuota(err, "collections") {
		t.Fatalf("while a is in use: %v, want a quota error", err)
	}
	if err := deleteDoc(db, "s", "a", a.ID); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, db, "b", `{}`)
}

func TestLoweringALimitBlocksOnlyNewInserts(t *testing.T) {
	db := docsDB(t)
	a := mustInsert(t, db, "a", `{}`)
	mustInsert(t, db, "a", `{}`)
	mustInsert(t, db, "b", `{}`)
	setLimits(t, db, 1, 1, nil) // usage: 2 collections, 2 documents in a
	if _, err := insertDoc(db, "s", "a", json.RawMessage(`{}`)); !isQuota(err, "documents") {
		t.Errorf("insert over the lowered docs limit: %v", err)
	}
	if _, err := insertDoc(db, "s", "c", json.RawMessage(`{}`)); !isQuota(err, "collections") {
		t.Errorf("insert over the lowered collections limit: %v", err)
	}
	if _, err := replaceDoc(db, "s", "a", a.ID, json.RawMessage(`{"n":1}`)); err != nil {
		t.Errorf("replace must still work: %v", err)
	}
	if _, err := getDoc(db, "s", "a", a.ID); err != nil {
		t.Errorf("get must still work: %v", err)
	}
	if err := deleteDoc(db, "s", "a", a.ID); err != nil {
		t.Errorf("delete must still work: %v", err)
	}
}

func TestZeroLimitsRefuseEverything(t *testing.T) {
	db := docsDB(t)
	setLimits(t, db, 0, 0, nil)
	if _, err := insertDoc(db, "s", "a", json.RawMessage(`{}`)); !isQuota(err, "collections") {
		t.Errorf("insert: %v", err)
	}
	if _, _, err := putKV(db, "s", "k", json.RawMessage(`1`)); !isQuota(err, "entries") {
		t.Errorf("kv put: %v", err)
	}
}

func TestSitesDoNotShareDocuments(t *testing.T) {
	db := docsDB(t)
	mustExec(t, db, `INSERT INTO sites(name, owner_id) VALUES('t', 1)`)
	setLimits(t, db, 1, 1, nil)
	d := mustInsert(t, db, "a", `{}`)
	if _, err := insertDoc(db, "t", "a", json.RawMessage(`{}`)); err != nil {
		t.Errorf("site t has its own caps: %v", err)
	}
	if _, err := getDoc(db, "t", "a", d.ID); !errors.Is(err, errNotFound) {
		t.Errorf("site t read site s's document: %v", err)
	}
}

func TestInsertForAMissingSiteFails(t *testing.T) {
	db := docsDB(t)
	if _, err := insertDoc(db, "nope", "a", json.RawMessage(`{}`)); err == nil {
		t.Error("insert for an unclaimed site must fail (foreign key)")
	}
}

func TestFailedInsertLeavesNoPartialState(t *testing.T) {
	db := docsDB(t)
	old := newID
	t.Cleanup(func() { newID = old })
	newID = func() (string, error) { return "SAME", nil }
	mustInsert(t, db, "a", `{}`)
	// the same id again: the insert hits the primary key after the cap checks passed
	if _, err := insertDoc(db, "s", "a", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a repeated id must fail")
	}
	if n := count(t, db, `SELECT count(*) FROM docs`); n != 1 {
		t.Errorf("%d documents, want 1", n)
	}
	newID = old
	if _, err := insertDoc(db, "s", "a", json.RawMessage(`{}`)); err != nil {
		t.Errorf("the store must still work after a failure: %v", err)
	}
}

func TestConcurrentInsertsRespectTheCaps(t *testing.T) {
	db := docsDB(t)
	setLimits(t, db, 3, 10, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := insertDoc(db, "s", fmt.Sprintf("c%d", i%6), json.RawMessage(`{}`))
			var q *quotaError
			switch {
			case err == nil:
				mu.Lock()
				ok++
				mu.Unlock()
			case !errors.As(err, &q):
				t.Errorf("insert: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if c := count(t, db, `SELECT count(DISTINCT collection) FROM docs`); c != 3 {
		t.Errorf("%d collections, want exactly the cap of 3", c)
	}
	rows, err := db.Query(`SELECT count(*) FROM docs GROUP BY collection`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var n int
		rows.Scan(&n)
		if n > 10 {
			t.Errorf("a collection holds %d documents, cap is 10", n)
		}
		total += n
	}
	if total != ok {
		t.Errorf("%d documents stored but %d inserts reported success", total, ok)
	}
}

func TestCountCollectionsHopQuery(t *testing.T) {
	db := docsDB(t)
	for _, c := range []string{"b", "a", "a", "c", "c", "c"} {
		mustInsert(t, db, c, `{}`)
	}
	putKV(db, "s", "k", json.RawMessage(`1`))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if n, err := countCollections(tx, "s"); err != nil || n != 3 {
		t.Errorf("countCollections = %d, %v; want 3 (the _kv entry is not a collection)", n, err)
	}
	if n, err := countCollections(tx, "empty"); err != nil || n != 0 {
		t.Errorf("a site with no documents: %d, %v; want 0", n, err)
	}
}
