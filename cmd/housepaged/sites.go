package main

import (
	"database/sql"
	"errors"

	"housepage/internal/rules"
)

type claimOutcome int

const (
	claimedNew   claimOutcome = iota // the name was free and is now the user's
	alreadyOwned                     // the user already owns it, so publishing replaces the site
	takenByOther                     // someone else owns the name
	atSiteLimit                      // the name is free but the user owns as many sites as they may
)

type claimResult struct {
	outcome     claimOutcome
	used, limit int // the user's site count and limit; set for claimedNew and atSiteLimit
}

// claimSite gives a name to a user if it is free and they are under their site limit. The checks and
// the insert are one transaction, and the pool has one connection, so two publishes cannot both slip
// under the limit.
func claimSite(db *sql.DB, name string, userID int64) (claimResult, error) {
	tx, err := db.Begin()
	if err != nil {
		return claimResult{}, err
	}
	defer tx.Rollback()

	var ownerID int64
	err = tx.QueryRow(`SELECT owner_id FROM sites WHERE name = ?`, name).Scan(&ownerID)
	switch {
	case err == nil && ownerID == userID:
		return claimResult{outcome: alreadyOwned}, nil
	case err == nil:
		return claimResult{outcome: takenByOther}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return claimResult{}, err
	}

	var max sql.NullInt64
	if err := tx.QueryRow(`SELECT max_sites FROM users WHERE id = ?`, userID).Scan(&max); err != nil {
		return claimResult{}, err
	}
	limit := rules.DefaultMaxSites
	if max.Valid {
		limit = int(max.Int64)
	}
	var used int
	if err := tx.QueryRow(`SELECT count(*) FROM sites WHERE owner_id = ?`, userID).Scan(&used); err != nil {
		return claimResult{}, err
	}
	if used >= limit {
		return claimResult{outcome: atSiteLimit, used: used, limit: limit}, nil
	}
	if _, err := tx.Exec(`INSERT INTO sites(name, owner_id) VALUES(?, ?)`, name, userID); err != nil {
		return claimResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return claimResult{}, err
	}
	return claimResult{outcome: claimedNew, used: used + 1, limit: limit}, nil
}

// siteOwnerID returns the owner of a site, or errNotFound if the name is unclaimed.
func siteOwnerID(db *sql.DB, name string) (int64, error) {
	var id int64
	err := db.QueryRow(`SELECT owner_id FROM sites WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errNotFound
	}
	return id, err
}

func sitesOfUser(db *sql.DB, userID int64) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sites WHERE owner_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// releaseSite frees a name. The site's settings and documents go with it (foreign key cascade).
func releaseSite(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM sites WHERE name = ?`, name)
	return err
}
