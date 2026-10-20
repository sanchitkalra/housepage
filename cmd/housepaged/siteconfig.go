package main

import (
	"database/sql"
	"strconv"
	"strings"

	"housepage/internal/rules"
)

// optInt is a limit in an update: not sent, or sent as a number, or sent as null (restore the default).
type optInt struct {
	set bool
	val *int
}

// String is for the log: "-" if unchanged, "default" if restored, else the number.
func (o optInt) String() string {
	switch {
	case !o.set:
		return "-"
	case o.val == nil:
		return "default"
	}
	return strconv.Itoa(*o.val)
}

// configPatch is an update to a site's data settings. Only the fields that are set are changed.
type configPatch struct {
	mode           *string // nil: unchanged
	maxCollections optInt
	maxDocs        optInt
	maxDocSize     optInt
}

func (p configPatch) modeString() string {
	if p.mode == nil {
		return "-"
	}
	return *p.mode
}

// setSiteConfig applies a patch, creating the settings row (mode off, every limit default) if the site has none.
func setSiteConfig(db *sql.DB, site string, p configPatch) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO site_db_config(site) VALUES(?) ON CONFLICT(site) DO NOTHING`, site); err != nil {
		return err
	}
	var sets []string // column names are fixed here, never taken from a request
	var args []any
	if p.mode != nil {
		sets, args = append(sets, "mode = ?"), append(args, *p.mode)
	}
	for _, f := range []struct {
		col string
		v   optInt
	}{{"max_collections", p.maxCollections}, {"max_docs", p.maxDocs}, {"max_doc_size", p.maxDocSize}} {
		if !f.v.set {
			continue
		}
		var v any // a nil pointer becomes NULL, which means the default
		if f.v.val != nil {
			v = *f.v.val
		}
		sets, args = append(sets, f.col+" = ?"), append(args, v)
	}
	if len(sets) > 0 {
		if _, err := tx.Exec(`UPDATE site_db_config SET `+strings.Join(sets, ", ")+` WHERE site = ?`, append(args, site)...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// siteUsage is how much of its allowance a site is using.
type siteUsage struct {
	Collections           int            `json:"collections"`
	DocumentsByCollection map[string]int `json:"documentsByCollection"`
	KVEntries             int            `json:"kvEntries"`
}

// getSiteUsage counts a site's documents per collection.
// ponytail: this reads every row of the site (the owner asks rarely); keep a counter if it ever gets slow.
func getSiteUsage(db *sql.DB, site string) (siteUsage, error) {
	u := siteUsage{DocumentsByCollection: map[string]int{}}
	rows, err := db.Query(`SELECT collection, count(*) FROM docs WHERE site = ? GROUP BY collection`, site)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return u, err
		}
		if name == rules.KVCollection {
			u.KVEntries = n
			continue
		}
		u.DocumentsByCollection[name] = n
	}
	u.Collections = len(u.DocumentsByCollection)
	return u, rows.Err()
}
