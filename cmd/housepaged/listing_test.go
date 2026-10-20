package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"housepage/internal/rules"
)

func listDB(t *testing.T) *sql.DB {
	t.Helper()
	db := docsDB(t)
	setLimits(t, db, 10, 20000, nil)
	return db
}

func idsOf(items []doc) []string {
	out := []string{}
	for _, d := range items {
		out = append(out, d.ID)
	}
	return out
}

// firstPage runs the first request of a listing and walkFrom follows the cursor, the way a client does.
func firstPage(t *testing.T, db *sql.DB, collection, sort string, desc bool, limit int) page {
	t.Helper()
	p, err := listDocs(db, "s", collection, sort, desc, limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func nextPage(t *testing.T, db *sql.DB, collection string, p page, limit int) page {
	t.Helper()
	cur, err := rules.DecodeCursor(p.Next)
	if err != nil {
		t.Fatalf("nextCursor %q: %v", p.Next, err)
	}
	n, err := listDocs(db, "s", collection, "", false, limit, &cur)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// walk lists everything page by page and returns the ids in the order received.
func walk(t *testing.T, db *sql.DB, collection, sort string, desc bool, limit int) []string {
	t.Helper()
	p := firstPage(t, db, collection, sort, desc, limit)
	all := idsOf(p.Items)
	for pages := 0; p.Next != ""; pages++ {
		if pages > 1000 {
			t.Fatal("paging does not end")
		}
		p = nextPage(t, db, collection, p, limit)
		all = append(all, idsOf(p.Items)...)
	}
	return all
}

func insertN(t *testing.T, db *sql.DB, collection string, bodies ...string) []string {
	t.Helper()
	var out []string
	for _, b := range bodies {
		out = append(out, mustInsert(t, db, collection, b).ID)
	}
	return out
}

func reversed(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

func TestListDefaultOrderIsCreationOrder(t *testing.T) {
	db := listDB(t)
	var bodies []string
	for i := 0; i < 10; i++ {
		bodies = append(bodies, fmt.Sprintf(`{"n":%d}`, 10-i)) // values run against creation order
	}
	ids := insertN(t, db, "c", bodies...)
	insertN(t, db, "other", `{}`)
	for _, limit := range []int{1, 3, 4, 10, 11} {
		if got := walk(t, db, "c", "", false, limit); !reflect.DeepEqual(got, ids) {
			t.Errorf("asc limit %d: got %v, want %v", limit, got, ids)
		}
		if got := walk(t, db, "c", "", true, limit); !reflect.DeepEqual(got, reversed(ids)) {
			t.Errorf("desc limit %d: got %v, want %v", limit, got, reversed(ids))
		}
	}
}

func TestListLastPageHasNoCursor(t *testing.T) {
	db := listDB(t)
	insertN(t, db, "c", `{}`, `{}`, `{}`, `{}`)
	if p := firstPage(t, db, "c", "", false, 4); len(p.Items) != 4 || p.Next != "" {
		t.Errorf("exact fit: %d items, next %q; want 4 and no cursor", len(p.Items), p.Next)
	}
	p := firstPage(t, db, "c", "", false, 3)
	if len(p.Items) != 3 || p.Next == "" {
		t.Fatalf("limit 3 of 4: %d items, next %q", len(p.Items), p.Next)
	}
	if last := nextPage(t, db, "c", p, 3); len(last.Items) != 1 || last.Next != "" {
		t.Errorf("last page: %d items, next %q", len(last.Items), last.Next)
	}
	if p := firstPage(t, db, "nothing", "", false, 5); len(p.Items) != 0 || p.Next != "" {
		t.Errorf("empty collection: %+v", p)
	}
	if p := firstPage(t, db, "nothing", "title", false, 5); len(p.Items) != 0 || p.Next != "" {
		t.Errorf("empty collection, sorted: %+v", p)
	}
}

func TestListSortedByNestedPathBothDirections(t *testing.T) {
	db := listDB(t)
	ids := insertN(t, db, "e",
		`{"when":{"start":"2026-03-04"}}`, // 0
		`{"when":{"start":"2026-01-12"}}`, // 1
		`{"when":{"start":"2026-02-01"}}`, // 2
	)
	wantAsc := []string{ids[1], ids[2], ids[0]}
	for _, limit := range []int{1, 2, 3} {
		if got := walk(t, db, "e", "when.start", false, limit); !reflect.DeepEqual(got, wantAsc) {
			t.Errorf("asc limit %d: %v, want %v", limit, got, wantAsc)
		}
		if got := walk(t, db, "e", "when.start", true, limit); !reflect.DeepEqual(got, reversed(wantAsc)) {
			t.Errorf("desc limit %d: %v, want %v", limit, got, reversed(wantAsc))
		}
	}
}

func TestListMissingValuesComeLastInEitherDirection(t *testing.T) {
	db := listDB(t)
	ids := insertN(t, db, "c",
		`{}`,               // 0 no field
		`{"t":"b"}`,        // 1
		`{"t":null}`,       // 2 JSON null counts as no value
		`{"t":"a"}`,        // 3
		`{"x":1}`,          // 4 other field only
		`{"t":{"deep":1}}`, // 5 an object has a value (its JSON text)
	)
	// values: a(3), b(1), {"deep":1}(5) in text order '{' < 'a'? '{' is 0x7b, after 'b'; see want below
	wantAsc := []string{ids[3], ids[1], ids[5], ids[0], ids[2], ids[4]}
	wantDesc := []string{ids[5], ids[1], ids[3], ids[4], ids[2], ids[0]}
	for _, limit := range []int{1, 2, 4, 6} {
		if got := walk(t, db, "c", "t", false, limit); !reflect.DeepEqual(got, wantAsc) {
			t.Errorf("asc limit %d: %v, want %v", limit, got, wantAsc)
		}
		if got := walk(t, db, "c", "t", true, limit); !reflect.DeepEqual(got, wantDesc) {
			t.Errorf("desc limit %d: %v, want %v", limit, got, wantDesc)
		}
	}
}

func TestListNumbersSortNumerically(t *testing.T) {
	db := listDB(t)
	ids := insertN(t, db, "c", `{"n":10}`, `{"n":2}`, `{"n":-5}`, `{"n":2.5}`, `{"n":1e2}`, `{"n":9007199254740993}`)
	want := []string{ids[2], ids[1], ids[3], ids[0], ids[4], ids[5]}
	for _, limit := range []int{1, 2, 6} {
		if got := walk(t, db, "c", "n", false, limit); !reflect.DeepEqual(got, want) {
			t.Errorf("asc limit %d: %v, want %v (2 must come before 10)", limit, got, want)
		}
		if got := walk(t, db, "c", "n", true, limit); !reflect.DeepEqual(got, reversed(want)) {
			t.Errorf("desc limit %d: %v, want %v", limit, got, reversed(want))
		}
	}
}

func TestListEqualValuesPageByIDWithoutRepeats(t *testing.T) {
	db := listDB(t)
	var bodies []string
	for i := 0; i < 9; i++ {
		bodies = append(bodies, fmt.Sprintf(`{"g":%d}`, i%3)) // three groups of three equal values
	}
	ids := insertN(t, db, "c", bodies...)
	var want []string
	for g := 0; g < 3; g++ {
		for i := g; i < 9; i += 3 {
			want = append(want, ids[i])
		}
	}
	for _, limit := range []int{1, 2, 4} {
		if got := walk(t, db, "c", "g", false, limit); !reflect.DeepEqual(got, want) {
			t.Errorf("asc limit %d: %v, want %v", limit, got, want)
		}
	}
	// descending flips the tie-break as well
	var wantDesc []string
	for g := 2; g >= 0; g-- {
		for i := 9 - 3 + g; i >= 0; i -= 3 {
			wantDesc = append(wantDesc, ids[i])
		}
	}
	for _, limit := range []int{1, 2, 4} {
		if got := walk(t, db, "c", "g", true, limit); !reflect.DeepEqual(got, wantDesc) {
			t.Errorf("desc limit %d: %v, want %v", limit, got, wantDesc)
		}
	}
}

func TestListTextComparesOnItsFirst256Characters(t *testing.T) {
	db := listDB(t)
	same := strings.Repeat("é", 256) // 256 characters, 512 bytes: the limit counts characters
	ids := insertN(t, db, "c",
		`{"t":"`+same+`z"}`,
		`{"t":"`+same+`a"}`,
		`{"t":"`+same+`"}`,
		`{"t":"`+strings.Repeat("é", 255)+`"}`,
	)
	// the first three tie on their 256-character prefix, so they come in id order; the short one is smaller
	want := []string{ids[3], ids[0], ids[1], ids[2]}
	for _, limit := range []int{1, 2, 4} {
		if got := walk(t, db, "c", "t", false, limit); !reflect.DeepEqual(got, want) {
			t.Errorf("limit %d: %v, want %v", limit, got, want)
		}
	}
	p := firstPage(t, db, "c", "t", false, 1)
	cur, err := rules.DecodeCursor(p.Next)
	if err != nil || len(cur.Value.(string)) != 255*2 {
		t.Errorf("the cursor should carry the 255-character value: err=%v", err)
	}
	p = firstPage(t, db, "c", "t", false, 2)
	if cur, _ = rules.DecodeCursor(p.Next); len([]rune(cur.Value.(string))) != 256 {
		t.Errorf("cursor value has %d characters, want 256 (cut)", len([]rune(cur.Value.(string))))
	}
}

func TestListCursorKeepsItsOwnSortAndOrder(t *testing.T) {
	db := listDB(t)
	insertN(t, db, "c", `{"n":1}`, `{"n":2}`, `{"n":3}`)
	p := firstPage(t, db, "c", "n", true, 1)
	cur, _ := rules.DecodeCursor(p.Next)
	// the arguments passed alongside a cursor are ignored: the cursor decides
	got, err := listDocs(db, "s", "c", "other", false, 1, &cur)
	if err != nil || string(got.Items[0].Data) != `{"n":2}` {
		t.Errorf("got %+v, %v; want the second-highest n", got, err)
	}
}

// The required test: a document added before the cursor's position never shows up on a later page, and
// nothing already seen is repeated. A document added after the position is still found.
func TestListDocumentsAddedBeforeTheCursorNeverRepeat(t *testing.T) {
	cases := []struct {
		name          string
		sort          string
		desc          bool
		before, after string // bodies of documents added mid-walk
	}{
		{"default asc", "", false, "", `{"n":0}`},          // new ULIDs sort after: never "before" in asc
		{"default desc", "", true, `{"n":0}`, ""},          // new ULIDs sort first in desc, i.e. before the cursor
		{"sorted asc", "n", false, `{"n":-1}`, `{"n":99}`}, // smaller value is before, bigger is after
		{"sorted desc", "n", true, `{"n":99}`, `{"n":-1}`},
		{"sorted asc, missing last", "n", false, `{"n":-1}`, `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := listDB(t)
			var bodies []string
			for i := 1; i <= 6; i++ {
				bodies = append(bodies, fmt.Sprintf(`{"n":%d}`, i*10))
			}
			insertN(t, db, "c", bodies...)
			start := walk(t, db, "c", c.sort, c.desc, 100) // the order before anything is added

			p := firstPage(t, db, "c", c.sort, c.desc, 2)
			seen := idsOf(p.Items)
			var added, addedAfter []string
			for i := 0; i < 2; i++ { // a few inserts between pages
				if c.before != "" {
					added = append(added, insertN(t, db, "c", c.before)...)
				}
				if c.after != "" {
					addedAfter = append(addedAfter, insertN(t, db, "c", c.after)...)
				}
			}
			for pages := 0; p.Next != ""; pages++ {
				if pages > 100 {
					t.Fatal("paging does not end")
				}
				p = nextPage(t, db, "c", p, 2)
				seen = append(seen, idsOf(p.Items)...)
			}

			count := map[string]int{}
			for _, id := range seen {
				count[id]++
			}
			for id, n := range count {
				if n != 1 {
					t.Errorf("%s was returned %d times", id, n)
				}
			}
			for _, id := range added {
				if count[id] != 0 {
					t.Errorf("document %s was added before the cursor but showed up", id)
				}
			}
			for _, id := range append(append([]string{}, start[2:]...), addedAfter...) {
				if count[id] != 1 {
					t.Errorf("document %s was after the cursor but arrived %d times", id, count[id])
				}
			}
		})
	}
}

func TestListSurvivesDeletingTheLastSeenDocument(t *testing.T) {
	db := listDB(t)
	for i, sort := range []string{"", "n"} {
		coll := fmt.Sprintf("c%d", i)
		ids := insertN(t, db, coll, `{"n":1}`, `{"n":2}`, `{"n":3}`, `{"n":4}`)
		p := firstPage(t, db, coll, sort, false, 2)
		if err := deleteDoc(db, "s", coll, p.Items[1].ID); err != nil {
			t.Fatal(err)
		}
		next := nextPage(t, db, coll, p, 2)
		if got, want := idsOf(next.Items), ids[2:]; !reflect.DeepEqual(got, want) {
			t.Errorf("sort %q: %v, want %v", sort, got, want)
		}
	}
}

func TestListOldCursorsStayUsable(t *testing.T) {
	db := listDB(t)
	insertN(t, db, "c", `{"n":1}`, `{"n":2}`, `{"n":3}`)
	p := firstPage(t, db, "c", "n", false, 1)
	a := nextPage(t, db, "c", p, 1)
	b := nextPage(t, db, "c", p, 1)
	if !reflect.DeepEqual(idsOf(a.Items), idsOf(b.Items)) {
		t.Error("the same cursor gave different pages")
	}
}

func TestListDocsDoesNotReadOtherSitesOrKV(t *testing.T) {
	db := listDB(t)
	mustExec(t, db, `INSERT INTO sites(name, owner_id) VALUES('t', 1)`)
	mine := insertN(t, db, "c", `{"n":1}`)
	if _, err := insertDoc(db, "t", "c", json.RawMessage(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	putKV(db, "s", "k", json.RawMessage(`{"n":3}`))
	for _, sort := range []string{"", "n"} {
		if got := walk(t, db, "c", sort, false, 10); !reflect.DeepEqual(got, mine) {
			t.Errorf("sort %q: %v, want only %v", sort, got, mine)
		}
	}
}

func TestListSortPathIsABoundParameter(t *testing.T) {
	db := listDB(t)
	insertN(t, db, "c", `{"a":1}`)
	// ValidSortPath rejects these at the API. Here they only prove the path never reaches the SQL text:
	// whether SQLite takes one as a path or refuses it, the table must be untouched.
	for _, path := range []string{`a') OR 1=1 --`, `a"b`, `'; DROP TABLE docs; --`} {
		listDocs(db, "s", "c", path, false, 5, nil)
	}
	if n := count(t, db, `SELECT count(*) FROM docs`); n != 1 {
		t.Errorf("docs table damaged: %d rows", n)
	}
}

func kvKeys(t *testing.T, db *sql.DB, prefix string, limit int) []string {
	t.Helper()
	p, err := listKV(db, "s", prefix, limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := idsOf(p.Items)
	for pages := 0; p.Next != ""; pages++ {
		if pages > 1000 {
			t.Fatal("paging does not end")
		}
		cur, err := rules.DecodeCursor(p.Next)
		if err != nil {
			t.Fatal(err)
		}
		if p, err = listKV(db, "s", "ignored", limit, &cur); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, idsOf(p.Items)...)
	}
	return keys
}

func TestListKVInKeyOrderWithPrefix(t *testing.T) {
	db := listDB(t)
	for _, k := range []string{"score:bob", "b", "score:amy", "a", "scorf", "score", "score:~", "zz"} {
		putKV(db, "s", k, json.RawMessage(`1`))
	}
	insertN(t, db, "c", `{}`)
	all := []string{"a", "b", "score", "score:amy", "score:bob", "score:~", "scorf", "zz"}
	for _, limit := range []int{1, 3, 8, 9} {
		if got := kvKeys(t, db, "", limit); !reflect.DeepEqual(got, all) {
			t.Errorf("all, limit %d: %v, want %v", limit, got, all)
		}
	}
	want := []string{"score:amy", "score:bob", "score:~"}
	for _, limit := range []int{1, 2, 3} {
		if got := kvKeys(t, db, "score:", limit); !reflect.DeepEqual(got, want) {
			t.Errorf("prefix score:, limit %d: %v, want %v", limit, got, want)
		}
	}
	if got := kvKeys(t, db, "nothing", 5); len(got) != 0 {
		t.Errorf("no match: %v", got)
	}
}

func TestListKVCursorKeepsItsPrefix(t *testing.T) {
	db := listDB(t)
	for _, k := range []string{"a1", "a2", "a3", "b1"} {
		putKV(db, "s", k, json.RawMessage(`1`))
	}
	p, err := listKV(db, "s", "a", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := rules.DecodeCursor(p.Next)
	next, err := listKV(db, "s", "b", 5, &cur) // a different prefix sent with the cursor is ignored
	if err != nil || !reflect.DeepEqual(idsOf(next.Items), []string{"a2", "a3"}) {
		t.Errorf("got %v, %v; want a2 a3", idsOf(next.Items), err)
	}
}

func TestListKVReturnsValuesOfAnyJSONType(t *testing.T) {
	db := listDB(t)
	putKV(db, "s", "n", json.RawMessage(`42`))
	putKV(db, "s", "o", json.RawMessage(`{"a":[1,2]}`))
	p, err := listKV(db, "s", "", 10, nil)
	if err != nil || string(p.Items[0].Data) != `42` || string(p.Items[1].Data) != `{"a":[1,2]}` {
		t.Errorf("%+v, %v", p, err)
	}
}
