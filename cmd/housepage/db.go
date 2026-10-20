package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"

	"housepage/internal/rules"
)

const dbUsage = `usage:
  housepage db config <site> [--mode off|open|owner] [--max-collections N|default] [--max-docs N|default] [--max-doc-size N|default]
  housepage db show <site>`

func dbCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(dbUsage)
	}
	switch args[0] {
	case "config":
		return dbConfig(args[1:])
	case "show":
		return dbShow(args[1:])
	}
	return fmt.Errorf("unknown command %q\n%s", "db "+args[0], dbUsage)
}

type limits struct{ MaxCollections, MaxDocs, MaxDocSize int }

type dbSettings struct {
	Site     string
	Mode     string
	Limits   limits
	Ceilings limits
	Usage    struct {
		Collections           int
		DocumentsByCollection map[string]int
		KVEntries             int
	}
}

func fetchSettings(method, site string, body []byte) (dbSettings, error) {
	var s dbSettings
	c, err := loadConfig()
	if err != nil {
		return s, err
	}
	var r io.Reader
	contentType := ""
	if body != nil {
		r, contentType = bytes.NewReader(body), "application/json"
	}
	out, err := request(c, method, "/api/v1/sites/"+site+"/db-config", r, contentType)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(out, &s); err != nil || s.Mode == "" {
		return s, fmt.Errorf("unexpected server reply: %s", out)
	}
	return s, nil
}

func printSettings(s dbSettings) {
	fmt.Fprintf(stdout, "site         %s\n", s.Site)
	fmt.Fprintf(stdout, "mode         %s\n", s.Mode)
	fmt.Fprintf(stdout, "collections  %d of %d (ceiling %d)\n", s.Usage.Collections, s.Limits.MaxCollections, s.Ceilings.MaxCollections)
	fmt.Fprintf(stdout, "documents    up to %d per collection (ceiling %d)\n", s.Limits.MaxDocs, s.Ceilings.MaxDocs)
	names := make([]string, 0, len(s.Usage.DocumentsByCollection))
	for n := range s.Usage.DocumentsByCollection {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(stdout, "  %-10s %d\n", n, s.Usage.DocumentsByCollection[n])
	}
	fmt.Fprintf(stdout, "kv entries   %d of %d\n", s.Usage.KVEntries, s.Limits.MaxDocs)
	fmt.Fprintf(stdout, "doc size     up to %d bytes (ceiling %d)\n", s.Limits.MaxDocSize, s.Ceilings.MaxDocSize)
}

func dbShow(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: housepage db show <site>")
	}
	if err := rules.ValidName(args[0]); err != nil {
		return err
	}
	s, err := fetchSettings("GET", args[0], nil)
	if err != nil {
		return err
	}
	printSettings(s)
	return nil
}

// dbConfig sends only the flags that were given, so the rest of the site's settings stay as they are.
// The site comes first, because Go's flag parsing stops at the first argument that is not a flag.
func dbConfig(args []string) error {
	const use = "usage: housepage db config <site> [--mode off|open|owner] [--max-collections N|default] [--max-docs N|default] [--max-doc-size N|default]"
	if len(args) == 0 || len(args[0]) > 0 && args[0][0] == '-' {
		return errors.New(use)
	}
	site := args[0]
	flags := flag.NewFlagSet("db config", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "who may use the site's data from its pages: off, open (read and write) or owner (read only)")
	maxCollections := flags.String("max-collections", "", "collections the site may hold, or default")
	maxDocs := flags.String("max-docs", "", "documents per collection, and key-value entries, or default")
	maxDocSize := flags.String("max-doc-size", "", "bytes per document or value, or default")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(use)
	}
	if err := rules.ValidName(site); err != nil {
		return err
	}
	body := map[string]any{}
	var bad error
	// A limit is a whole number, or "default", which is sent as null to restore the server's default.
	limit := func(name, field, raw string, check func(int) error) {
		if raw == "default" {
			body[field] = nil
			return
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			bad = fmt.Errorf("--%s must be a whole number or default; got %q", name, raw)
			return
		}
		if err := check(n); err != nil {
			bad = err
			return
		}
		body[field] = n
	}
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mode":
			body["mode"] = *mode
		case "max-collections":
			limit(f.Name, "maxCollections", *maxCollections, rules.CheckMaxCollections)
		case "max-docs":
			limit(f.Name, "maxDocs", *maxDocs, rules.CheckMaxDocs)
		case "max-doc-size":
			limit(f.Name, "maxDocSize", *maxDocSize, rules.CheckMaxDocSize)
		}
	})
	if bad != nil {
		return bad
	}
	if len(body) == 0 {
		return errors.New("nothing to change; give at least one flag\n" + use)
	}
	if m, ok := body["mode"].(string); ok && m != "off" && m != "open" && m != "owner" {
		return fmt.Errorf("invalid mode %q: use off, open or owner", m)
	}
	payload, _ := json.Marshal(body)
	s, err := fetchSettings("PUT", site, payload)
	if err != nil {
		return err
	}
	printSettings(s)
	return nil
}
