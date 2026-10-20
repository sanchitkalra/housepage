package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type reply struct {
	code   int
	raw    string
	header http.Header
	obj    map[string]any
	arr    []any
}

func api(t *testing.T, srv *httptest.Server, method, path, token, body string) reply {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := reply{code: resp.StatusCode, raw: string(raw), header: resp.Header}
	json.Unmarshal(raw, &r.obj)
	json.Unmarshal(raw, &r.arr)
	return r
}

func (r reply) errCode() string {
	s, _ := r.obj["error"].(string)
	return s
}

// at digs into a decoded JSON object: at(r.obj, "user", "name").
func at(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		next, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = next[k]
	}
	return cur
}

type apiFixture struct {
	srv       *httptest.Server
	db        *sql.DB
	bob, amy  string // ordinary default tokens
	root      string // a superuser token (the default token of the superuser "root")
	rootDaily string // an ordinary token of the same superuser
}

func newAPIFixture(t *testing.T) apiFixture {
	t.Helper()
	srv, _, tok, db := newTestServerWithDB(t)
	root, rootToken, err := createUser(db, "root", true)
	if err != nil {
		t.Fatal(err)
	}
	daily, err := mintToken(db, root.ID, "daily", false)
	if err != nil {
		t.Fatal(err)
	}
	return apiFixture{srv: srv, db: db, bob: tok["bob"], amy: tok["amy"], root: rootToken, rootDaily: daily}
}

func TestNewRoutesAnswerUnauthenticatedRequestsWithJSON(t *testing.T) {
	f := newAPIFixture(t)
	routes := [][2]string{
		{"POST", "/api/v1/users"}, {"GET", "/api/v1/users/bob"}, {"PUT", "/api/v1/users/bob"},
		{"POST", "/api/v1/tokens"}, {"GET", "/api/v1/tokens"}, {"DELETE", "/api/v1/tokens/default"},
	}
	for _, rt := range routes {
		for _, token := range []string{"", "hp_nope"} {
			r := api(t, f.srv, rt[0], rt[1], token, `{}`)
			if r.code != 401 || r.errCode() != "unauthorized" || r.header.Get("WWW-Authenticate") != "Bearer" ||
				!strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
				t.Errorf("%s %s with token %q: %d %q header %v", rt[0], rt[1], token, r.code, r.raw, r.header)
			}
		}
	}
}

func TestCreateUserNeedsASuperuserToken(t *testing.T) {
	f := newAPIFixture(t)
	for name, token := range map[string]string{"ordinary user": f.bob, "a superuser's ordinary token": f.rootDaily} {
		if r := api(t, f.srv, "POST", "/api/v1/users", token, `{"name":"sam"}`); r.code != 403 || r.errCode() != "forbidden" {
			t.Errorf("%s: %d %s", name, r.code, r.raw)
		}
	}
	if n := count(t, f.db, `SELECT count(*) FROM users WHERE name = 'sam'`); n != 0 {
		t.Error("a refused request still created the user")
	}

	r := api(t, f.srv, "POST", "/api/v1/users", f.root, `{"name":"sam"}`)
	if r.code != 201 || at(r.obj, "user", "name") != "sam" || at(r.obj, "user", "isSuperuser") != false ||
		at(r.obj, "user", "maxSites") != float64(10) || at(r.obj, "token", "label") != "default" ||
		at(r.obj, "token", "isSuperuser") != false {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	token, _ := at(r.obj, "token", "token").(string)
	if !strings.HasPrefix(token, "hp_") {
		t.Fatalf("no token in the response: %s", r.raw)
	}
	if got := api(t, f.srv, "GET", "/api/v1/tokens", token, ""); got.code != 200 {
		t.Errorf("the returned default token does not work: %d %s", got.code, got.raw)
	}
	if c, _ := lookupToken(f.db, token); c.UserName != "sam" || c.TokenLabel != "default" {
		t.Errorf("the token belongs to %+v", c)
	}
}

func TestASuperuserCanCreateAnotherSuperuser(t *testing.T) {
	f := newAPIFixture(t)
	r := api(t, f.srv, "POST", "/api/v1/users", f.root, `{"name":"deputy","superuser":true}`)
	if r.code != 201 || at(r.obj, "user", "isSuperuser") != true || at(r.obj, "token", "isSuperuser") != true {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	token, _ := at(r.obj, "token", "token").(string)
	if c, err := lookupToken(f.db, token); err != nil || !c.IsSuperuser || c.UserName != "deputy" {
		t.Errorf("the new superuser's default token: %+v (%v)", c, err)
	}
	// and that token can create users in turn
	if r := api(t, f.srv, "POST", "/api/v1/users", token, `{"name":"zed"}`); r.code != 201 {
		t.Errorf("%d %s", r.code, r.raw)
	}
}

func TestCreateUserValidatesTheRequest(t *testing.T) {
	f := newAPIFixture(t)
	before := count(t, f.db, `SELECT count(*) FROM users`)
	cases := []struct {
		name, body string
		code       int
		err        string
	}{
		{"duplicate", `{"name":"bob"}`, 409, "user_exists"},
		{"blank name", `{"name":"   "}`, 400, "invalid_user_name"},
		{"missing name", `{}`, 400, "invalid_user_name"},
		{"empty body", ``, 400, "invalid_json"},
		{"not json", `name=sam`, 400, "invalid_json"},
		{"unknown field", `{"name":"sam","admin":true}`, 400, "invalid_request"},
		{"wrong type", `{"name":5}`, 400, "invalid_request"},
		{"trailing data", `{"name":"sam"} {}`, 400, "invalid_json"},
	}
	for _, c := range cases {
		if r := api(t, f.srv, "POST", "/api/v1/users", f.root, c.body); r.code != c.code || r.errCode() != c.err || r.obj["message"] == "" {
			t.Errorf("%s: %d %s, want %d %s", c.name, r.code, r.raw, c.code, c.err)
		}
	}
	if n := count(t, f.db, `SELECT count(*) FROM users`); n != before {
		t.Errorf("%d users, want %d: a rejected request created one", n, before)
	}
}

func TestGetUser(t *testing.T) {
	f := newAPIFixture(t)
	for _, name := range []string{"one", "two"} {
		if code, _ := put(t, f.srv, name, f.bob, indexOnly); code != 200 {
			t.Fatal(code)
		}
	}

	r := api(t, f.srv, "GET", "/api/v1/users/bob", f.bob, "")
	if r.code != 200 || at(r.obj, "name") != "bob" || at(r.obj, "maxSites") != float64(10) || at(r.obj, "sitesUsed") != float64(2) ||
		at(r.obj, "isSuperuser") != false || at(r.obj, "createdAt") == nil {
		t.Errorf("own user: %d %s", r.code, r.raw)
	}
	for _, name := range []string{"amy", "nobody"} {
		if r := api(t, f.srv, "GET", "/api/v1/users/"+name, f.bob, ""); r.code != 403 || r.errCode() != "forbidden" {
			t.Errorf("an ordinary token reading %q: %d %s, want the same 403 whether or not the user exists", name, r.code, r.raw)
		}
	}
	if r := api(t, f.srv, "GET", "/api/v1/users/bob", f.rootDaily, ""); r.code != 403 {
		t.Errorf("a superuser's ordinary token reading someone else: %d", r.code)
	}
	if r := api(t, f.srv, "GET", "/api/v1/users/bob", f.root, ""); r.code != 200 || at(r.obj, "sitesUsed") != float64(2) {
		t.Errorf("a superuser token reading bob: %d %s", r.code, r.raw)
	}
	if r := api(t, f.srv, "GET", "/api/v1/users/nobody", f.root, ""); r.code != 404 || r.errCode() != "not_found" {
		t.Errorf("a superuser token reading a missing user: %d %s", r.code, r.raw)
	}
	if r := api(t, f.srv, "GET", "/api/v1/users/root", f.rootDaily, ""); r.code != 200 || at(r.obj, "isSuperuser") != true {
		t.Errorf("any token may read its own user: %d %s", r.code, r.raw)
	}
}

func TestUserNamesWithSpacesWork(t *testing.T) {
	f := newAPIFixture(t)
	r := api(t, f.srv, "POST", "/api/v1/users", f.root, `{"name":"Sam Lee"}`)
	token, _ := at(r.obj, "token", "token").(string)
	if got := api(t, f.srv, "GET", "/api/v1/users/Sam%20Lee", token, ""); got.code != 200 || at(got.obj, "name") != "Sam Lee" {
		t.Errorf("%d %s", got.code, got.raw)
	}
}

func TestPutUserChangesTheSiteLimit(t *testing.T) {
	f := newAPIFixture(t)
	for name, token := range map[string]string{"the user themselves": f.bob, "a superuser's ordinary token": f.rootDaily} {
		if r := api(t, f.srv, "PUT", "/api/v1/users/bob", token, `{"maxSites":99}`); r.code != 403 || r.errCode() != "forbidden" {
			t.Errorf("%s: %d %s", name, r.code, r.raw)
		}
	}
	if u, _ := getUserByName(f.db, "bob"); u.MaxSites != nil {
		t.Fatal("a refused request changed the limit")
	}

	r := api(t, f.srv, "PUT", "/api/v1/users/bob", f.root, `{"maxSites":1}`)
	if r.code != 200 || at(r.obj, "maxSites") != float64(1) || at(r.obj, "sitesUsed") != float64(0) {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	if code, _ := put(t, f.srv, "first", f.bob, indexOnly); code != 200 {
		t.Fatal(code)
	}
	if code, body := put(t, f.srv, "second", f.bob, indexOnly); code != 403 || !strings.Contains(body, "1 of 1") {
		t.Errorf("the new limit is not applied to publishing: %d %q", code, body)
	}

	if r := api(t, f.srv, "PUT", "/api/v1/users/bob", f.root, `{"maxSites":null}`); r.code != 200 || at(r.obj, "maxSites") != float64(10) {
		t.Errorf("null should restore the default: %d %s", r.code, r.raw)
	}
	if r := api(t, f.srv, "PUT", "/api/v1/users/bob", f.root, `{"maxSites":0}`); r.code != 200 || at(r.obj, "maxSites") != float64(0) {
		t.Errorf("0 is a valid limit: %d %s", r.code, r.raw)
	}
}

func TestPutUserValidatesTheRequest(t *testing.T) {
	f := newAPIFixture(t)
	cases := []struct {
		name, path, body, err string
		code                  int
	}{
		{"negative", "bob", `{"maxSites":-1}`, "invalid_request", 400},
		{"a string", "bob", `{"maxSites":"5"}`, "invalid_request", 400},
		{"a fraction", "bob", `{"maxSites":2.5}`, "invalid_request", 400},
		{"nothing to change", "bob", `{}`, "invalid_request", 400},
		{"promotion is not an API action", "bob", `{"isSuperuser":true}`, "invalid_request", 400},
		{"not json", "bob", `maxSites=3`, "invalid_json", 400},
		{"unknown user", "nobody", `{"maxSites":3}`, "not_found", 404},
	}
	for _, c := range cases {
		if r := api(t, f.srv, "PUT", "/api/v1/users/"+c.path, f.root, c.body); r.code != c.code || r.errCode() != c.err {
			t.Errorf("%s: %d %s, want %d %s", c.name, r.code, r.raw, c.code, c.err)
		}
	}
	if u, _ := getUserByName(f.db, "bob"); u.MaxSites != nil || u.IsSuperuser {
		t.Errorf("a rejected request changed bob: %+v", u)
	}
}

func TestMintATokenForYourself(t *testing.T) {
	f := newAPIFixture(t)
	r := api(t, f.srv, "POST", "/api/v1/tokens", f.bob, `{"label":"ci"}`)
	if r.code != 201 || at(r.obj, "label") != "ci" || at(r.obj, "isSuperuser") != false || at(r.obj, "createdAt") == nil {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	token, _ := at(r.obj, "token").(string)
	if c, err := lookupToken(f.db, token); err != nil || c.UserName != "bob" || c.TokenLabel != "ci" || c.IsSuperuser {
		t.Errorf("the new token resolves to %+v (%v)", c, err)
	}

	cases := []struct {
		name, body, err string
		code            int
	}{
		{"duplicate label", `{"label":"ci"}`, "label_exists", 409},
		{"invalid label", `{"label":"bad label"}`, "invalid_label", 400},
		{"missing label", `{}`, "invalid_label", 400},
		{"cannot pick another user", `{"label":"x","user":"amy"}`, "invalid_request", 400},
		{"a superuser token from an ordinary token", `{"label":"adm","superuser":true}`, "forbidden", 403},
	}
	for _, c := range cases {
		if r := api(t, f.srv, "POST", "/api/v1/tokens", f.bob, c.body); r.code != c.code || r.errCode() != c.err {
			t.Errorf("%s: %d %s, want %d %s", c.name, r.code, r.raw, c.code, c.err)
		}
	}
	if n := count(t, f.db, `SELECT count(*) FROM tokens t JOIN users u ON u.id = t.user_id WHERE u.name = 'bob'`); n != 2 {
		t.Errorf("bob has %d tokens, want 2: a refused request minted one", n)
	}
}

func TestOnlyASuperuserTokenMintsASuperuserToken(t *testing.T) {
	f := newAPIFixture(t)
	// root is a superuser, but this token is ordinary
	if r := api(t, f.srv, "POST", "/api/v1/tokens", f.rootDaily, `{"label":"admin2","superuser":true}`); r.code != 403 || r.errCode() != "forbidden" {
		t.Errorf("an ordinary token of a superuser: %d %s", r.code, r.raw)
	}
	r := api(t, f.srv, "POST", "/api/v1/tokens", f.root, `{"label":"admin2","superuser":true}`)
	if r.code != 201 || at(r.obj, "isSuperuser") != true {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	token, _ := at(r.obj, "token").(string)
	if c, err := lookupToken(f.db, token); err != nil || !c.IsSuperuser || c.UserName != "root" {
		t.Errorf("%+v (%v)", c, err)
	}
	// a superuser token can still mint an ordinary one for everyday use
	if r := api(t, f.srv, "POST", "/api/v1/tokens", f.root, `{"label":"laptop"}`); r.code != 201 || at(r.obj, "isSuperuser") != false {
		t.Errorf("%d %s", r.code, r.raw)
	}
}

func TestListYourTokens(t *testing.T) {
	f := newAPIFixture(t)
	r := api(t, f.srv, "POST", "/api/v1/tokens", f.bob, `{"label":"ci"}`)
	ci, _ := at(r.obj, "token").(string)

	list := api(t, f.srv, "GET", "/api/v1/tokens", f.bob, "")
	if list.code != 200 || len(list.arr) != 2 {
		t.Fatalf("%d %s", list.code, list.raw)
	}
	first, second := list.arr[0].(map[string]any), list.arr[1].(map[string]any)
	if first["label"] != "default" || first["current"] != true || second["label"] != "ci" || second["current"] != false {
		t.Errorf("listing with the default token: %s", list.raw)
	}
	if again := api(t, f.srv, "GET", "/api/v1/tokens", ci, ""); again.arr[0].(map[string]any)["current"] != false || again.arr[1].(map[string]any)["current"] != true {
		t.Errorf("listing with the ci token should mark ci as current: %s", again.raw)
	}
	for _, secret := range []string{f.bob, ci, hashToken(f.bob), hashToken(ci)} {
		if strings.Contains(list.raw, secret) {
			t.Error("a listing shows a token or its hash")
		}
	}
	if other := api(t, f.srv, "GET", "/api/v1/tokens", f.amy, ""); len(other.arr) != 1 || strings.Contains(other.raw, "ci") {
		t.Errorf("amy sees %s", other.raw)
	}
}

func TestRevokeYourTokens(t *testing.T) {
	f := newAPIFixture(t)
	r := api(t, f.srv, "POST", "/api/v1/tokens", f.bob, `{"label":"ci"}`)
	ci, _ := at(r.obj, "token").(string)

	if r := api(t, f.srv, "DELETE", "/api/v1/tokens/ci", f.bob, ""); r.code != 204 || r.raw != "" {
		t.Fatalf("%d %q", r.code, r.raw)
	}
	if got := api(t, f.srv, "GET", "/api/v1/tokens", ci, ""); got.code != 401 {
		t.Errorf("a revoked token still works: %d", got.code)
	}
	if got := api(t, f.srv, "GET", "/api/v1/tokens", f.bob, ""); got.code != 200 {
		t.Errorf("revoking ci broke the default token: %d", got.code)
	}
	for _, label := range []string{"ci", "nosuch", "bad%20label"} {
		if r := api(t, f.srv, "DELETE", "/api/v1/tokens/"+label, f.bob, ""); r.code != 404 || r.errCode() != "not_found" {
			t.Errorf("revoking %q: %d %s", label, r.code, r.raw)
		}
	}

	// both bob and amy have a token labelled "default"; revoking bob's, the one in use, is allowed
	if r := api(t, f.srv, "DELETE", "/api/v1/tokens/default", f.bob, ""); r.code != 204 {
		t.Fatalf("revoking the token in use: %d %s", r.code, r.raw)
	}
	if got := api(t, f.srv, "GET", "/api/v1/tokens", f.bob, ""); got.code != 401 {
		t.Errorf("the revoked default token still works: %d", got.code)
	}
	if got := api(t, f.srv, "GET", "/api/v1/tokens", f.amy, ""); got.code != 200 {
		t.Errorf("amy's token was revoked along with bob's: %d", got.code)
	}
}
