package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*httptest.Server, string, map[string]string) {
	t.Helper()
	d := t.TempDir()
	db, err := openDB(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tokens := map[string]string{}
	for _, owner := range []string{"bob", "amy"} {
		if tokens[owner], err = createToken(db, owner); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer((&server{db: db, dataDir: d, scheme: "http", domain: "example.test"}).routes())
	t.Cleanup(srv.Close)
	return srv, d, tokens
}

func put(t *testing.T, srv *httptest.Server, name, token string, files map[string]string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for p, c := range files {
		w, err := mw.CreateFormFile("files", p)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(c))
	}
	mw.Close()
	req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/sites/"+name, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestPutSite(t *testing.T) {
	srv, dir, tok := newTestServer(t)
	site := map[string]string{"index.html": "v1", "css/app.css": "body{}", "old.txt": "x"}

	if code, _ := put(t, srv, "timer", "", site); code != 401 {
		t.Errorf("no token: got %d, want 401", code)
	}
	if code, _ := put(t, srv, "timer", "hp_nope", site); code != 401 {
		t.Errorf("bad token: got %d, want 401", code)
	}

	code, body := put(t, srv, "timer", tok["bob"], site)
	if code != 200 || !strings.Contains(body, "http://timer.example.test/") {
		t.Fatalf("publish: got %d %q", code, body)
	}
	if got := read(t, filepath.Join(dir, "sites/timer/css/app.css")); got != "body{}" {
		t.Errorf("nested file = %q", got)
	}

	if code, _ := put(t, srv, "timer", tok["amy"], map[string]string{"index.html": "hijack"}); code != 403 {
		t.Errorf("other user's name: got %d, want 403", code)
	}
	if got := read(t, filepath.Join(dir, "sites/timer/index.html")); got != "v1" {
		t.Errorf("site changed by a non-owner: %q", got)
	}

	if code, _ := put(t, srv, "timer", tok["bob"], map[string]string{"index.html": "v2"}); code != 200 {
		t.Fatalf("owner republish: got %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "sites/timer/old.txt")); !os.IsNotExist(err) {
		t.Errorf("old.txt survived a republish: %v", err)
	}
}

func TestBadUploadsAreRejectedAndDoNotClaimTheName(t *testing.T) {
	srv, dir, tok := newTestServer(t)
	bad := map[string]map[string]string{
		"evil":   {"index.html": "x", "../x": "y"},
		"nopage": {"a.html": "x"},
		"App":    {"index.html": "x"},
		"www":    {"index.html": "x"},
	}
	for name, files := range bad {
		if code, _ := put(t, srv, name, tok["bob"], files); code != 400 {
			t.Errorf("%s: got %d, want 400", name, code)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); !os.IsNotExist(err) {
		t.Errorf("traversal wrote outside sites/: %v", err)
	}
	if code, _ := put(t, srv, "evil", tok["amy"], map[string]string{"index.html": "mine"}); code != 200 {
		t.Errorf("a rejected upload claimed the name: amy got %d", code)
	}

	req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/sites/plain", strings.NewReader("not multipart"))
	req.Header.Set("Authorization", "Bearer "+tok["bob"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("non-multipart body: got %d, want 400", resp.StatusCode)
	}
}

func call(t *testing.T, method, url, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestListAndDelete(t *testing.T) {
	srv, dir, tok := newTestServer(t)
	idx := map[string]string{"index.html": "x"}
	put(t, srv, "alpha", tok["bob"], idx)
	put(t, srv, "bravo", tok["bob"], idx)
	put(t, srv, "amys-site", tok["amy"], idx)

	code, body := call(t, "GET", srv.URL+"/api/v1/sites", tok["bob"])
	if code != 200 || !strings.Contains(body, "alpha") || !strings.Contains(body, "bravo") || strings.Contains(body, "amys-site") {
		t.Errorf("bob's list: %d %s", code, body)
	}
	if code, _ := call(t, "GET", srv.URL+"/api/v1/sites", ""); code != 401 {
		t.Errorf("list without token: got %d, want 401", code)
	}

	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/sites/alpha", tok["amy"]); code != 403 {
		t.Errorf("delete someone else's site: got %d, want 403", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "sites/alpha/index.html")); err != nil {
		t.Errorf("site removed by a non-owner: %v", err)
	}
	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/sites/nothere", tok["bob"]); code != 404 {
		t.Errorf("delete unknown: got %d, want 404", code)
	}
	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/sites/..%2Fsites", tok["bob"]); code != 400 {
		t.Errorf("delete traversal name: got %d, want 400", code)
	}
	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/sites/alpha", tok["bob"]); code != 200 {
		t.Fatalf("owner delete: got %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "sites/alpha")); !os.IsNotExist(err) {
		t.Errorf("files survived delete: %v", err)
	}
	if code, _ := put(t, srv, "alpha", tok["amy"], idx); code != 200 {
		t.Errorf("name was not freed by delete: amy got %d", code)
	}
}
