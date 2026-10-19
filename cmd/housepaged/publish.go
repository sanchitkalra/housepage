package main

import (
	"os"
	"path/filepath"

	"housepage/internal/rules"
)

type file struct {
	path string
	data []byte
}

func check(name string, files []file) error {
	if err := rules.ValidName(name); err != nil {
		return err
	}
	entries := make([]rules.Entry, len(files))
	for i, f := range files {
		entries[i] = rules.Entry{Path: f.path, Size: int64(len(f.data))}
	}
	return rules.CheckFiles(entries)
}

// write replaces sites/<name> with files; callers must check() first.
// ponytail: wipe-then-write, so visitors can briefly see a half-written site; swap a symlink for atomic publishes.
func write(dataDir, name string, files []file) error {
	dir := filepath.Join(dataDir, "sites", name)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for _, f := range files {
		dst := filepath.Join(dir, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, f.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func publish(dataDir, name string, files []file) error {
	if err := check(name, files); err != nil {
		return err
	}
	return write(dataDir, name, files)
}
