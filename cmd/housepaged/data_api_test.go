package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// dataFixture has bob's site "bobsite" and amy's site "amysite", both with no settings row (mode off).
type dataFixture struct {
	apiFixture
}

func newDataFixture(t *testing.T) dataFixture {
	t.Helper()
	f := newAPIFixture(t)
	for site, owner := range map[string]string{"bobsite": "bob", "amysite": "amy"} {
		u, err := getUserByName(f.db, owner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := claimSite(f.db, site, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	return dataFixture{f}
}

func (f dataFixture) mode(t *testing.T, site, mode string) {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO site_db_config(site, mode) VALUES(?, ?) ON CONFLICT(site) DO UPDATE SET mode = excluded.mode`, site, mode)
}

func (f dataFixture) limits(t *testing.T, site string, mc, md, ms any) {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO site_db_config(site, mode, max_collections, max_docs, max_doc_size) VALUES(?, 'open', ?, ?, ?)
		ON CONFLICT(site) DO UPDATE SET max_collections = excluded.max_collections, max_docs = excluded.max_docs, max_doc_size = excluded.max_doc_size`,
		site, mc, md, ms)
}

// owner calls the app-host route with a token; page calls the site-host route the way nginx forwards it.
func (f dataFixture) owner(t *testing.T, token, site, method, suffix, body string) reply {
	t.Helper()
	return api(t, f.srv, method, "/api/v1/sites/"+site+suffix, token, body)
}

func (f dataFixture) page(t *testing.T, site, method, suffix, body string) reply {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+"/_api"+suffix, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if site != "" {
		req.Header.Set("X-Housepage-Site", site)
	}
	return doRequest(t, req)
}

func doRequest(t *testing.T, req *http.Request) reply {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return readReply(resp)
}

func readReply(resp *http.Response) reply {
	raw, _ := io.ReadAll(resp.Body)
	r := reply{code: resp.StatusCode, raw: string(raw), header: resp.Header}
	json.Unmarshal(raw, &r.obj)
	json.Unmarshal(raw, &r.arr)
	return r
}

func isJSONError(r reply, code int, errCode string) bool {
	return r.code == code && r.errCode() == errCode && strings.HasPrefix(r.header.Get("Content-Type"), "application/json")
}

func itemIDs(r reply) []string {
	var out []string
	items, _ := r.obj["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

func TestDataModeMatrixOnTheSiteHost(t *testing.T) {
	f := newDataFixture(t)
	routes := []struct {
		method, suffix, body string
		write                bool
	}{
		{"GET", "/kv/k", "", false},
		{"GET", "/kv", "", false},
		{"GET", "/db/c", "", false},
		{"GET", "/db/c/NOPE", "", false},
		{"PUT", "/kv/k", `1`, true},
		{"DELETE", "/kv/k", "", true},
		{"POST", "/db/c", `{}`, true},
		{"PUT", "/db/c/NOPE", `{}`, true},
		{"DELETE", "/db/c/NOPE", "", true},
	}
	for _, mode := range []string{"", "off", "owner", "open"} { // "" is no settings row at all
		if mode != "" {
			f.mode(t, "bobsite", mode)
		}
		for _, rt := range routes {
			r := f.page(t, "bobsite", rt.method, rt.suffix, rt.body)
			switch {
			case mode == "" || mode == "off":
				if !isJSONError(r, 403, "db_disabled") {
					t.Errorf("mode %q %s %s: %d %s, want 403 db_disabled", mode, rt.method, rt.suffix, r.code, r.raw)
				}
			case mode == "owner" && rt.write:
				if !isJSONError(r, 403, "read_only") {
					t.Errorf("mode owner %s %s: %d %s, want 403 read_only", rt.method, rt.suffix, r.code, r.raw)
				}
			default:
				if r.code == 403 || r.code >= 500 {
					t.Errorf("mode %s %s %s: %d %s, want it allowed", mode, rt.method, rt.suffix, r.code, r.raw)
				}
			}
		}
	}
}

func TestOwnerMayAlwaysUseTheAppHostRoutes(t *testing.T) {
	f := newDataFixture(t)
	for _, mode := range []string{"off", "owner", "open"} {
		f.mode(t, "bobsite", mode)
		if r := f.owner(t, f.bob, "bobsite", "PUT", "/kv/k", `"v"`); r.code != 201 && r.code != 200 {
			t.Errorf("mode %s write: %d %s", mode, r.code, r.raw)
		}
		if r := f.owner(t, f.bob, "bobsite", "GET", "/kv/k", ""); r.code != 200 {
			t.Errorf("mode %s read: %d %s", mode, r.code, r.raw)
		}
	}
}

func TestAppHostRoutesNeedTheOwnersToken(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	for _, token := range []string{"", "hp_nope"} {
		if r := f.owner(t, token, "bobsite", "GET", "/kv", ""); !isJSONError(r, 401, "unauthorized") {
			t.Errorf("token %q: %d %s", token, r.code, r.raw)
		}
	}
	for name, token := range map[string]string{"amy": f.amy, "root": f.root} { // another user, even a superuser
		if r := f.owner(t, token, "bobsite", "GET", "/kv", ""); !isJSONError(r, 403, "forbidden") {
			t.Errorf("%s on bob's site: %d %s, want 403 forbidden", name, r.code, r.raw)
		}
		if r := f.owner(t, token, "bobsite", "PUT", "/kv/k", `1`); !isJSONError(r, 403, "forbidden") {
			t.Errorf("%s writing bob's site: %d %s", name, r.code, r.raw)
		}
	}
	for _, site := range []string{"nosuchsite", "UPPER", "a"} {
		if r := f.owner(t, f.bob, site, "GET", "/kv", ""); !isJSONError(r, 404, "not_found") {
			t.Errorf("site %q: %d %s, want 404 not_found", site, r.code, r.raw)
		}
	}
}

func TestSiteHostNeedsAValidSiteHeader(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	for _, site := range []string{"", "UPPER", "a", "-bad-"} {
		if r := f.page(t, site, "GET", "/kv", ""); !isJSONError(r, 400, "invalid_request") {
			t.Errorf("header %q: %d %s", site, r.code, r.raw)
		}
	}
	// a name nobody has claimed has no settings, so it is simply off
	if r := f.page(t, "unclaimed", "GET", "/kv", ""); !isJSONError(r, 403, "db_disabled") {
		t.Errorf("unclaimed: %d %s", r.code, r.raw)
	}
}

func TestSiteHostIgnoresAnyToken(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "owner")
	req, _ := http.NewRequest("PUT", f.srv.URL+"/_api/kv/k", strings.NewReader(`1`))
	req.Header.Set("X-Housepage-Site", "bobsite")
	req.Header.Set("Authorization", "Bearer "+f.bob)
	if r := doRequest(t, req); !isJSONError(r, 403, "read_only") {
		t.Errorf("owner token on the site host: %d %s; the mode alone decides there", r.code, r.raw)
	}
}

func TestKVOverHTTP(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	r := f.page(t, "bobsite", "PUT", "/kv/score", `42`)
	if r.code != 201 || at(r.obj, "id") != "score" || at(r.obj, "data") != float64(42) || at(r.obj, "createdAt") == nil || at(r.obj, "updatedAt") == nil {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "PUT", "/kv/score?strategy=replace", `{"a":[1,"x"]}`); r.code != 200 || at(r.obj, "createdAt") != at(f.page(t, "bobsite", "GET", "/kv/score", "").obj, "createdAt") {
		t.Errorf("replace: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/kv/score", ""); r.code != 200 || at(r.obj, "data", "a") == nil {
		t.Errorf("get: %d %s", r.code, r.raw)
	}
	for i := 0; i < 2; i++ {
		if r := f.page(t, "bobsite", "DELETE", "/kv/score", ""); r.code != 204 || r.raw != "" {
			t.Errorf("delete #%d: %d %q", i, r.code, r.raw)
		}
	}
	if r := f.page(t, "bobsite", "GET", "/kv/score", ""); !isJSONError(r, 404, "not_found") {
		t.Errorf("get after delete: %d %s", r.code, r.raw)
	}
}

func TestKVErrors(t *testing.T) {
	f := newDataFixture(t)
	f.limits(t, "bobsite", nil, 1, 20)
	cases := []struct {
		name, method, suffix, body string
		status                     int
		code                       string
	}{
		{"bad key", "PUT", "/kv/bad%20key", `1`, 400, "invalid_key"},
		{"bad key on get", "GET", "/kv/bad%2Fkey", "", 400, "invalid_key"},
		{"empty body", "PUT", "/kv/k", ``, 400, "invalid_json"},
		{"not json", "PUT", "/kv/k", `{nope`, 400, "invalid_json"},
		{"two values", "PUT", "/kv/k", `1 2`, 400, "invalid_json"},
		{"unknown strategy", "PUT", "/kv/k?strategy=merge", `1`, 400, "unknown_strategy"},
		{"too large", "PUT", "/kv/k", `"` + strings.Repeat("x", 30) + `"`, 413, "document_too_large"},
	}
	for _, c := range cases {
		if r := f.page(t, "bobsite", c.method, c.suffix, c.body); !isJSONError(r, c.status, c.code) {
			t.Errorf("%s: %d %s, want %d %s", c.name, r.code, r.raw, c.status, c.code)
		}
	}
	if r := f.page(t, "bobsite", "PUT", "/kv/k", `"x"`); r.code != 201 {
		t.Fatalf("first entry: %d %s", r.code, r.raw)
	}
	r := f.page(t, "bobsite", "PUT", "/kv/other", `"x"`)
	if !isJSONError(r, 409, "quota_exceeded") || !strings.Contains(r.raw, "key-value store is full") {
		t.Errorf("second entry: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "PUT", "/kv/k", `"y"`); r.code != 200 {
		t.Errorf("replace at the cap: %d %s", r.code, r.raw)
	}
	big := f.page(t, "bobsite", "PUT", "/kv/k", `"`+strings.Repeat("x", 30)+`"`)
	if !strings.Contains(big.raw, "value is 32 bytes; the limit is 20") {
		t.Errorf("size message: %s", big.raw)
	}
}

func TestDocumentsOverHTTP(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	r := f.page(t, "bobsite", "POST", "/db/events", `{"title":"Launch","when":{"start":"2026-03-04"}}`)
	id, _ := r.obj["id"].(string)
	if r.code != 201 || len(id) != 26 || at(r.obj, "data", "title") != "Launch" || r.obj["createdAt"] != r.obj["updatedAt"] {
		t.Fatalf("insert: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/db/events/"+id, ""); r.code != 200 || at(r.obj, "data", "when", "start") != "2026-03-04" {
		t.Errorf("get: %d %s", r.code, r.raw)
	}
	r = f.page(t, "bobsite", "PUT", "/db/events/"+id, `{"title":"Kickoff"}`)
	if r.code != 200 || at(r.obj, "data", "title") != "Kickoff" || at(r.obj, "data", "when") != nil {
		t.Errorf("replace: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "PUT", "/db/events/NOPE", `{}`); !isJSONError(r, 404, "not_found") {
		t.Errorf("replace of a missing id (no upsert): %d %s", r.code, r.raw)
	}
	for i := 0; i < 2; i++ {
		if r := f.page(t, "bobsite", "DELETE", "/db/events/"+id, ""); r.code != 204 {
			t.Errorf("delete #%d: %d", i, r.code)
		}
	}
	if r := f.page(t, "bobsite", "GET", "/db/events/"+id, ""); !isJSONError(r, 404, "not_found") {
		t.Errorf("get after delete: %d %s", r.code, r.raw)
	}
}

func TestDocumentErrors(t *testing.T) {
	f := newDataFixture(t)
	f.limits(t, "bobsite", 1, 1, 30)
	cases := []struct {
		name, method, suffix, body string
		status                     int
		code                       string
	}{
		{"reserved collection", "POST", "/db/_kv", `{}`, 400, "invalid_collection"},
		{"uppercase collection", "POST", "/db/Events", `{}`, 400, "invalid_collection"},
		{"bad collection on get", "GET", "/db/_kv/x", "", 400, "invalid_collection"},
		{"bad collection on list", "GET", "/db/Bad", "", 400, "invalid_collection"},
		{"not an object", "POST", "/db/c", `[1]`, 400, "not_an_object"},
		{"scalar", "POST", "/db/c", `42`, 400, "not_an_object"},
		{"not json", "POST", "/db/c", `{`, 400, "invalid_json"},
		{"empty", "POST", "/db/c", ``, 400, "invalid_json"},
		{"too large", "POST", "/db/c", `{"a":"` + strings.Repeat("x", 40) + `"}`, 413, "document_too_large"},
		{"unknown strategy", "PUT", "/db/c/X?strategy=merge", `{}`, 400, "unknown_strategy"},
		{"replace not an object", "PUT", "/db/c/X", `1`, 400, "not_an_object"},
	}
	for _, c := range cases {
		if r := f.page(t, "bobsite", c.method, c.suffix, c.body); !isJSONError(r, c.status, c.code) {
			t.Errorf("%s: %d %s, want %d %s", c.name, r.code, r.raw, c.status, c.code)
		}
	}
	if r := f.page(t, "bobsite", "POST", "/db/c", `{}`); r.code != 201 {
		t.Fatalf("first: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "POST", "/db/c", `{}`); !isJSONError(r, 409, "quota_exceeded") || !strings.Contains(r.raw, `collection \"c\" is full`) {
		t.Errorf("document cap: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "POST", "/db/d", `{}`); !isJSONError(r, 409, "quota_exceeded") || !strings.Contains(r.raw, `\"d\" would be a new one`) {
		t.Errorf("collection cap: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "POST", "/db/c", `{"a":"`+strings.Repeat("x", 40)+`"}`); !strings.Contains(r.raw, "document is 48 bytes; the limit is 30") {
		t.Errorf("size message: %s", r.raw)
	}
}

func TestRequestBodyBeyondTheReadLimitIsRefused(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	huge := `{"a":"` + strings.Repeat("x", 70<<10) + `"}`
	for _, rt := range [][2]string{{"POST", "/db/c"}, {"PUT", "/kv/k"}, {"PUT", "/db/c/X"}} {
		if r := f.page(t, "bobsite", rt[0], rt[1], huge); !isJSONError(r, 413, "document_too_large") || !strings.Contains(r.raw, "request body is over") {
			t.Errorf("%s %s: %d %.80s", rt[0], rt[1], r.code, r.raw)
		}
	}
}

func TestSitesAreSeparateOverHTTP(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	f.mode(t, "amysite", "open")
	f.page(t, "bobsite", "PUT", "/kv/k", `"bob"`)
	r := f.page(t, "bobsite", "POST", "/db/c", `{"who":"bob"}`)
	id := r.obj["id"].(string)
	if r := f.page(t, "amysite", "GET", "/kv/k", ""); r.code != 404 {
		t.Errorf("amy read bob's key: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "amysite", "GET", "/db/c/"+id, ""); r.code != 404 {
		t.Errorf("amy read bob's document: %d %s", r.code, r.raw)
	}
	f.page(t, "amysite", "DELETE", "/db/c/"+id, "")
	if r := f.page(t, "bobsite", "GET", "/db/c/"+id, ""); r.code != 200 {
		t.Errorf("amy's delete reached bob's document: %d", r.code)
	}
	if r := f.owner(t, f.bob, "bobsite", "GET", "/db/c/"+id, ""); r.code != 200 || at(r.obj, "data", "who") != "bob" {
		t.Errorf("owner route sees the same data: %d %s", r.code, r.raw)
	}
}

func TestListDocumentsOverHTTP(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	var ids []string
	for _, n := range []int{30, 10, 20, 40, 50} {
		r := f.page(t, "bobsite", "POST", "/db/c", fmt.Sprintf(`{"n":%d}`, n))
		ids = append(ids, r.obj["id"].(string))
	}
	f.page(t, "bobsite", "POST", "/db/c", `{}`)

	// creation order, by cursor alone
	r := f.page(t, "bobsite", "GET", "/db/c?limit=4", "")
	if r.code != 200 || fmt.Sprint(itemIDs(r)) != fmt.Sprint(ids[:4]) || r.obj["nextCursor"] == nil {
		t.Fatalf("first page: %d %s", r.code, r.raw)
	}
	r = f.page(t, "bobsite", "GET", "/db/c?limit=4&cursor="+url.QueryEscape(r.obj["nextCursor"].(string)), "")
	if len(itemIDs(r)) != 2 || r.obj["nextCursor"] != nil || !strings.Contains(r.raw, `"nextCursor":null`) {
		t.Errorf("last page: %s", r.raw)
	}

	// sorted descending, missing last, then the cursor alone keeps the sort
	r = f.page(t, "bobsite", "GET", "/db/c?sort=n&order=desc&limit=3", "")
	if r.code != 200 || fmt.Sprint(itemIDs(r)) != fmt.Sprint([]string{ids[4], ids[3], ids[0]}) {
		t.Fatalf("sorted page: %d %s", r.code, r.raw)
	}
	r = f.page(t, "bobsite", "GET", "/db/c?cursor="+url.QueryEscape(r.obj["nextCursor"].(string)), "")
	if got := itemIDs(r); len(got) != 3 || got[0] != ids[2] || got[1] != ids[1] {
		t.Errorf("second sorted page: %s", r.raw)
	}

	if r := f.page(t, "bobsite", "GET", "/db/nothing", ""); r.code != 200 || !strings.Contains(r.raw, `"items":[]`) || !strings.Contains(r.raw, `"nextCursor":null`) {
		t.Errorf("empty collection: %d %s", r.code, r.raw)
	}
}

func TestListDocumentsErrors(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	for i := 0; i < 3; i++ {
		f.page(t, "bobsite", "POST", "/db/c", fmt.Sprintf(`{"n":%d}`, i))
	}
	first := f.page(t, "bobsite", "GET", "/db/c?sort=n&order=asc&limit=1", "")
	cur := url.QueryEscape(first.obj["nextCursor"].(string))
	plain := f.page(t, "bobsite", "GET", "/db/c?limit=1", "")
	plainCur := url.QueryEscape(plain.obj["nextCursor"].(string))

	cases := []struct {
		name, suffix string
		status       int
		code         string
	}{
		{"limit zero", "/db/c?limit=0", 400, "invalid_limit"},
		{"limit too big", "/db/c?limit=101", 400, "invalid_limit"},
		{"limit text", "/db/c?limit=ten", 400, "invalid_limit"},
		{"bad order", "/db/c?order=up", 400, "invalid_order"},
		{"bad sort", "/db/c?sort=a..b", 400, "invalid_sort"},
		{"quoted sort", "/db/c?sort=" + url.QueryEscape(`a"b`), 400, "invalid_sort"},
		{"empty sort", "/db/c?sort=", 400, "invalid_sort"},
		{"garbage cursor", "/db/c?cursor=%21%21", 400, "invalid_cursor"},
		{"empty cursor", "/db/c?cursor=", 400, "invalid_cursor"},
		{"sort differs from cursor", "/db/c?sort=m&cursor=" + cur, 400, "cursor_mismatch"},
		{"order differs from cursor", "/db/c?order=desc&cursor=" + cur, 400, "cursor_mismatch"},
		{"sort on a creation-order cursor", "/db/c?sort=n&cursor=" + plainCur, 400, "cursor_mismatch"},
		{"bad limit with a cursor", "/db/c?limit=0&cursor=" + cur, 400, "invalid_limit"},
	}
	for _, c := range cases {
		if r := f.page(t, "bobsite", "GET", c.suffix, ""); !isJSONError(r, c.status, c.code) {
			t.Errorf("%s: %d %s, want %d %s", c.name, r.code, r.raw, c.status, c.code)
		}
	}
	f.page(t, "bobsite", "PUT", "/kv/a1", `1`)
	f.page(t, "bobsite", "PUT", "/kv/a2", `1`)
	kvCur := f.page(t, "bobsite", "GET", "/kv?prefix=a&limit=1", "").obj["nextCursor"].(string)
	if r := f.page(t, "bobsite", "GET", "/db/c?cursor="+url.QueryEscape(kvCur), ""); !isJSONError(r, 400, "invalid_cursor") {
		t.Errorf("prefix cursor on /db: %d %s", r.code, r.raw)
	}
	// the same sort and order sent with the cursor are fine
	if r := f.page(t, "bobsite", "GET", "/db/c?sort=n&order=asc&limit=1&cursor="+cur, ""); r.code != 200 {
		t.Errorf("matching sort and order with a cursor: %d %s", r.code, r.raw)
	}
	// a key listing's cursor and a document listing's cursor are not interchangeable where it matters
	if r := f.page(t, "bobsite", "GET", "/kv?cursor="+cur, ""); !isJSONError(r, 400, "invalid_cursor") {
		t.Errorf("sorted cursor on /kv: %d %s", r.code, r.raw)
	}
}

func TestListKVOverHTTP(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	for _, k := range []string{"score:bob", "score:amy", "other", "score:cy"} {
		f.page(t, "bobsite", "PUT", "/kv/"+k, `1`)
	}
	f.page(t, "bobsite", "POST", "/db/c", `{}`)

	r := f.page(t, "bobsite", "GET", "/kv?prefix=score:&limit=2", "")
	if fmt.Sprint(itemIDs(r)) != "[score:amy score:bob]" || r.obj["nextCursor"] == nil {
		t.Fatalf("first page: %d %s", r.code, r.raw)
	}
	cur := url.QueryEscape(r.obj["nextCursor"].(string))
	if r := f.page(t, "bobsite", "GET", "/kv?cursor="+cur, ""); fmt.Sprint(itemIDs(r)) != "[score:cy]" || r.obj["nextCursor"] != nil {
		t.Errorf("cursor alone keeps the prefix: %s", r.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/kv?prefix=score:&cursor="+cur, ""); r.code != 200 {
		t.Errorf("same prefix with the cursor: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/kv?prefix=other&cursor="+cur, ""); !isJSONError(r, 400, "cursor_mismatch") {
		t.Errorf("different prefix with the cursor: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/kv?prefix=bad%20prefix", ""); !isJSONError(r, 400, "invalid_key") {
		t.Errorf("bad prefix: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/kv", ""); len(itemIDs(r)) != 4 {
		t.Errorf("no prefix lists every key and no documents: %s", r.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/kv?limit=0", ""); !isJSONError(r, 400, "invalid_limit") {
		t.Errorf("limit 0: %d %s", r.code, r.raw)
	}
}

func TestDataRoutesRefuseOtherMethods(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	for _, rt := range [][2]string{{"PATCH", "/kv/k"}, {"POST", "/kv/k"}, {"PUT", "/db/c"}, {"DELETE", "/db/c"}, {"POST", "/db/c/X"}} {
		if r := f.page(t, "bobsite", rt[0], rt[1], `{}`); r.code != 405 {
			t.Errorf("%s %s: %d, want 405", rt[0], rt[1], r.code)
		}
	}
}
