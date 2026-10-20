package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"housepage/internal/rules"
)

// page is one page of a listing. Next is the cursor for the following page, or "" on the last page.
type page struct {
	Items []doc
	Next  string
}

// listDocs lists a collection, by creation order (ids are ULIDs) or sorted by a path into the document.
// Documents with no value at the sort path come last in either direction. With a cursor, the cursor's own
// sort and order are used and the sort and desc arguments are ignored. Pages are found by position
// ("after this value and id"), so a document added or removed elsewhere never shifts or repeats a page.
func listDocs(db *sql.DB, site, collection, sort string, desc bool, limit int, cur *rules.Cursor) (page, error) {
	if cur != nil {
		sort, desc = cur.Sort, cur.Order == "desc"
	}
	dir, cmp := "ASC", ">"
	if desc {
		dir, cmp = "DESC", "<"
	}

	var rows *sql.Rows
	var err error
	if sort == "" {
		q := `SELECT id, value, created_at, updated_at FROM docs WHERE site = ? AND collection = ?`
		args := []any{site, collection}
		if cur != nil {
			q += ` AND id ` + cmp + ` ?`
			args = append(args, cur.ID)
		}
		q += ` ORDER BY id ` + dir + ` LIMIT ?`
		rows, err = db.Query(q, append(args, limit+1)...)
	} else {
		// f is the sort value: the value at the path, with text cut to its first SortTextPrefix characters.
		// NULL (also a JSON null) means no value and sorts after everything. The path is a bound parameter.
		q := fmt.Sprintf(`SELECT id, value, created_at, updated_at, f FROM (
  SELECT id, value, created_at, updated_at, CASE WHEN typeof(x) = 'text' THEN substr(x, 1, %d) ELSE x END AS f
  FROM (SELECT id, value, created_at, updated_at, json_extract(value, ?) AS x FROM docs WHERE site = ? AND collection = ?))`, rules.SortTextPrefix)
		args := []any{rules.JSONPath(sort), site, collection}
		if cur != nil {
			if cur.NoValue {
				q += ` WHERE f IS NULL AND id ` + cmp + ` ?`
				args = append(args, cur.ID)
			} else {
				v, err := cursorBindValue(cur.Value)
				if err != nil {
					return page{}, err
				}
				q += ` WHERE (f IS NOT NULL AND (f ` + cmp + ` ? OR (f = ? AND id ` + cmp + ` ?))) OR f IS NULL`
				args = append(args, v, v, cur.ID)
			}
		}
		q += ` ORDER BY f IS NULL, f ` + dir + `, id ` + dir + ` LIMIT ?`
		rows, err = db.Query(q, append(args, limit+1)...)
	}
	if err != nil {
		return page{}, err
	}
	defer rows.Close()

	var items []doc
	var fs []any // the sort value of each row, parallel to items
	for rows.Next() {
		var d doc
		var text string
		var f any
		dest := []any{&d.ID, &text, &d.CreatedAt, &d.UpdatedAt}
		if sort != "" {
			dest = append(dest, &f)
		}
		if err := rows.Scan(dest...); err != nil {
			return page{}, err
		}
		d.Data = json.RawMessage(text)
		items = append(items, d)
		fs = append(fs, f)
	}
	if err := rows.Err(); err != nil {
		return page{}, err
	}
	if len(items) <= limit {
		return page{Items: items}, nil
	}
	items, lastF := items[:limit], fs[limit-1]
	next := rules.Cursor{Sort: sort, Order: dir2order(desc), ID: items[limit-1].ID}
	if sort != "" {
		if lastF == nil {
			next.NoValue = true
		} else if next.Value, err = cursorValue(lastF); err != nil {
			return page{}, err
		}
	}
	return page{Items: items, Next: rules.EncodeCursor(next)}, nil
}

// listKV lists key-value entries in key order, optionally only keys starting with prefix. With a cursor,
// the cursor's prefix is used and the prefix argument is ignored.
func listKV(db *sql.DB, site, prefix string, limit int, cur *rules.Cursor) (page, error) {
	if cur != nil {
		prefix = cur.Prefix
	}
	// Keys are ASCII below 0x7f, so every key with the prefix lies in [prefix, prefix+0x7f).
	q := `SELECT id, value, created_at, updated_at FROM docs WHERE site = ? AND collection = ? AND id >= ? AND id < ?`
	args := []any{site, rules.KVCollection, prefix, prefix + "\x7f"}
	if cur != nil {
		q += ` AND id > ?`
		args = append(args, cur.ID)
	}
	rows, err := db.Query(q+` ORDER BY id LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return page{}, err
	}
	defer rows.Close()
	var items []doc
	for rows.Next() {
		var d doc
		var text string
		if err := rows.Scan(&d.ID, &text, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return page{}, err
		}
		d.Data = json.RawMessage(text)
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return page{}, err
	}
	if len(items) <= limit {
		return page{Items: items}, nil
	}
	items = items[:limit]
	return page{Items: items, Next: rules.EncodeCursor(rules.Cursor{ID: items[limit-1].ID, Prefix: prefix})}, nil
}

func dir2order(desc bool) string {
	if desc {
		return "desc"
	}
	return "asc"
}

// cursorValue turns a sort value read from SQLite into what a cursor carries: a string or a json.Number.
func cursorValue(f any) (any, error) {
	switch v := f.(type) {
	case string:
		return v, nil
	case int64:
		return json.Number(strconv.FormatInt(v, 10)), nil
	case float64:
		return json.Number(strconv.FormatFloat(v, 'g', -1, 64)), nil
	}
	return nil, fmt.Errorf("unexpected sort value of type %T", f)
}

// cursorBindValue is the reverse: a cursor's value as something SQLite can compare against.
func cursorBindValue(v any) (any, error) {
	n, ok := v.(json.Number)
	if !ok {
		return v, nil // a string
	}
	if i, err := n.Int64(); err == nil {
		return i, nil
	}
	f, err := n.Float64()
	if err != nil {
		return nil, rules.ErrInvalidCursor
	}
	return f, nil
}
