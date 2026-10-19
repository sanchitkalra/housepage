package main

import (
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func mkfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectSkipsJunkAndRejectsSymlinks(t *testing.T) {
	d := t.TempDir()
	mkfile(t, filepath.Join(d, "index.html"), "x")
	mkfile(t, filepath.Join(d, "css/app.css"), "x")
	mkfile(t, filepath.Join(d, ".git/config"), "x")
	mkfile(t, filepath.Join(d, ".DS_Store"), "x")
	files, err := collect(d)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.rel)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "css/app.css,index.html" {
		t.Errorf("collected %v", got)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(d); err == nil {
		t.Error("symlink was accepted")
	}
}

func loginAs(t *testing.T, url string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	if err := saveConfig(config{Server: url, Token: "hp_test"}); err != nil {
		t.Fatal(err)
	}
}

func TestPublishSendsRelativePathsAndToken(t *testing.T) {
	var gotPaths []string
	var gotAuth, gotMethod, gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotURL = r.Header.Get("Authorization"), r.Method, r.URL.Path
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			_, params, _ := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
			gotPaths = append(gotPaths, params["filename"])
		}
		w.Write([]byte(`{"url":"http://demo.example.test/"}`))
	}))
	defer srv.Close()
	loginAs(t, srv.URL)
	d := t.TempDir()
	mkfile(t, filepath.Join(d, "index.html"), "x")
	mkfile(t, filepath.Join(d, "css/app.css"), "x")

	if err := publish([]string{d, "--name", "demo"}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(gotPaths)
	if strings.Join(gotPaths, ",") != "css/app.css,index.html" {
		t.Errorf("server received paths %v", gotPaths)
	}
	if gotAuth != "Bearer hp_test" || gotMethod != "PUT" || gotURL != "/api/v1/sites/demo" {
		t.Errorf("request was %s %s auth=%q", gotMethod, gotURL, gotAuth)
	}
}

func TestPublishFailsEarlyAndSurfacesServerErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `name "demo" is already taken`, http.StatusForbidden)
	}))
	defer srv.Close()
	loginAs(t, srv.URL)
	d := t.TempDir()
	mkfile(t, filepath.Join(d, "index.html"), "x")

	err := publish([]string{d, "--name", "demo"})
	if err == nil || !strings.Contains(err.Error(), "already taken") {
		t.Errorf("server message not surfaced: %v", err)
	}
	empty := t.TempDir()
	mkfile(t, filepath.Join(empty, "page.html"), "x")
	before := calls
	if err := publish([]string{empty, "--name", "demo"}); err == nil || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("missing index.html not caught locally: %v", err)
	}
	if err := publish([]string{d, "--name", "Bad_Name"}); err == nil {
		t.Error("invalid name not caught locally")
	}
	if calls != before {
		t.Errorf("client contacted the server for uploads it could reject itself")
	}
}

func TestHTMLErrorPagesBecomeReadableMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("<html><body><h1>429 Too Many Requests</h1></body></html>"))
	}))
	defer srv.Close()
	_, err := request(config{Server: srv.URL, Token: "hp_test"}, "GET", "/api/v1/sites", nil, "")
	if err == nil || !strings.Contains(err.Error(), "wait about 10 seconds") || strings.Contains(err.Error(), "<html>") {
		t.Errorf("HTML 429 not turned into a readable message: %v", err)
	}
}
