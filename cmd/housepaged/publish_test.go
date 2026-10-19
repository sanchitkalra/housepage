package main

import (
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPublishWritesAndReplaces(t *testing.T) {
	d := t.TempDir()
	v1 := []file{{"index.html", []byte("v1")}, {"css/app.css", []byte("body{}")}, {"old.txt", []byte("x")}}
	if err := publish(d, "timer", v1); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(d, "sites/timer/css/app.css")); got != "body{}" {
		t.Errorf("css = %q", got)
	}
	if err := publish(d, "timer", []file{{"index.html", []byte("v2")}}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(d, "sites/timer/index.html")); got != "v2" {
		t.Errorf("index = %q, want v2", got)
	}
	if _, err := os.Stat(filepath.Join(d, "sites/timer/old.txt")); !os.IsNotExist(err) {
		t.Errorf("old.txt survived a replace: %v", err)
	}
}

func TestPublishRejectsBadUploadWithoutTouchingExistingSite(t *testing.T) {
	d := t.TempDir()
	if err := publish(d, "timer", []file{{"index.html", []byte("keep me")}}); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]file{
		"timer":    {{"../escape.txt", []byte("x")}, {"index.html", []byte("x")}},
		"Bad_Name": {{"index.html", []byte("x")}},
	}
	for name, files := range bad {
		if publish(d, name, files) == nil {
			t.Errorf("publish(%q) succeeded, want error", name)
		}
	}
	if got := read(t, filepath.Join(d, "sites/timer/index.html")); got != "keep me" {
		t.Errorf("existing site changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(d, "sites/escape.txt")); !os.IsNotExist(err) {
		t.Errorf("traversal wrote outside the site dir: %v", err)
	}
}
