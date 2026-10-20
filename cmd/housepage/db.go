package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"housepage/internal/rules"
)

const dbUsage = `usage:
  housepage db config <site> [--mode off|open|owner] [--max-collections N] [--max-docs N] [--max-doc-size N]
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
	const use = "usage: housepage db config <site> [--mode off|open|owner] [--max-collections N] [--max-docs N] [--max-doc-size N]"
	if len(args) == 0 || len(args[0]) > 0 && args[0][0] == '-' {
		return errors.New(use)
	}
	site := args[0]
	flags := flag.NewFlagSet("db config", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "who may use the site's data from its pages: off, open (read and write) or owner (read only)")
	maxCollections := flags.Int("max-collections", 0, "collections the site may hold")
	maxDocs := flags.Int("max-docs", 0, "documents per collection, and key-value entries")
	maxDocSize := flags.Int("max-doc-size", 0, "bytes per document or value")
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
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mode":
			body["mode"] = *mode
		case "max-collections":
			body["maxCollections"] = *maxCollections
		case "max-docs":
			body["maxDocs"] = *maxDocs
		case "max-doc-size":
			body["maxDocSize"] = *maxDocSize
		}
	})
	if len(body) == 0 {
		return errors.New("nothing to change; give at least one flag\n" + use)
	}
	if m, ok := body["mode"].(string); ok && m != "off" && m != "open" && m != "owner" {
		return fmt.Errorf("invalid mode %q: use off, open or owner", m)
	}
	for name, check := range map[string]func(int) error{"maxCollections": rules.CheckMaxCollections, "maxDocs": rules.CheckMaxDocs, "maxDocSize": rules.CheckMaxDocSize} {
		if n, ok := body[name].(int); ok {
			if err := check(n); err != nil {
				return err
			}
		}
	}
	payload, _ := json.Marshal(body)
	s, err := fetchSettings("PUT", site, payload)
	if err != nil {
		return err
	}
	printSettings(s)
	return nil
}
