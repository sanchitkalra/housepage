// Package rules holds the publishing rules shared by client and server.
// The server must enforce them; the client only uses them to fail early.
package rules

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	MaxFiles      = 500
	MaxFileBytes  = 5 << 20
	MaxTotalBytes = 25 << 20
	maxPathLen    = 200
)

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	reserved = map[string]bool{"app": true, "www": true, "api": true, "admin": true, "mail": true, "ns1": true, "ns2": true}
)

type Entry struct {
	Path string
	Size int64
}

func ValidName(name string) error {
	switch {
	case !nameRe.MatchString(name):
		return fmt.Errorf("invalid name %q: use 3-63 lowercase letters, digits or hyphens, not starting or ending with a hyphen", name)
	case strings.HasPrefix(name, "xn--"):
		return fmt.Errorf("invalid name %q: names starting with xn-- are not allowed", name)
	case reserved[name]:
		return fmt.Errorf("name %q is reserved", name)
	}
	return nil
}

func ValidPath(p string) error {
	if p == "" || len(p) > maxPathLen {
		return fmt.Errorf("invalid path %q: must be 1-%d characters", p, maxPathLen)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return fmt.Errorf("invalid path %q: contains a control character or backslash", p)
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path %q: must be relative, with no empty, . or .. segments", p)
		}
	}
	return nil
}

func CheckFiles(files []Entry) error {
	if len(files) == 0 || len(files) > MaxFiles {
		return fmt.Errorf("a site needs 1-%d files, got %d", MaxFiles, len(files))
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range files {
		if err := ValidPath(f.Path); err != nil {
			return err
		}
		if seen[f.Path] {
			return fmt.Errorf("duplicate path %q", f.Path)
		}
		seen[f.Path] = true
		if f.Size > MaxFileBytes {
			return fmt.Errorf("%q is %d bytes; the limit is %d per file", f.Path, f.Size, MaxFileBytes)
		}
		total += f.Size
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("site is %d bytes in total; the limit is %d", total, MaxTotalBytes)
	}
	if !seen["index.html"] {
		return errors.New("index.html is required at the site root")
	}
	for p := range seen {
		for i := 0; i < len(p); i++ {
			if p[i] == '/' && seen[p[:i]] {
				return fmt.Errorf("%q is both a file and a directory", p[:i])
			}
		}
	}
	return nil
}
