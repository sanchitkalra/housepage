package rules

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// A cursor is the position of the last document a client received, written down so a listing can resume
// after it. The server keeps nothing: the cursor is base64url (no padding) of a small JSON object.

const maxCursorLen = 2048

// ErrInvalidCursor is returned for any cursor that is malformed, tampered with or inconsistent.
var ErrInvalidCursor = errors.New("the cursor is not valid")

type Cursor struct {
	Sort    string // the sort path; empty for creation order and for key-value listings
	Order   string // "asc" or "desc"; empty means asc
	NoValue bool   // sorted listings only: the last document had no value at Sort
	Value   any    // sorted listings only: the last sort value, a string or a json.Number
	ID      string // the last document's id, or the last key
	Prefix  string // key-value listings only: the prefix filter
}

type cursorWire struct {
	S string `json:"s,omitempty"`
	O string `json:"o,omitempty"`
	N *int   `json:"n,omitempty"`
	V any    `json:"v,omitempty"`
	I string `json:"i"`
	P string `json:"p,omitempty"`
}

// EncodeCursor turns a cursor into the opaque string a client sends back.
func EncodeCursor(c Cursor) string {
	w := cursorWire{S: c.Sort, O: c.Order, V: c.Value, I: c.ID, P: c.Prefix}
	if c.Sort == "" && c.Order == "asc" {
		w.O = "" // ascending is the default, so the common cursor stays short
	}
	if c.Sort != "" {
		n := 0
		if c.NoValue {
			n = 1
			w.V = nil
		}
		w.N = &n
	}
	b, _ := json.Marshal(w) // these field types cannot fail to marshal
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor reads a cursor a client sent back. Anything unexpected is ErrInvalidCursor.
func DecodeCursor(s string) (Cursor, error) {
	if s == "" || len(s) > maxCursorLen {
		return Cursor{}, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber() // keeps large integers exact
	var w cursorWire
	if err := dec.Decode(&w); err != nil || dec.More() {
		return Cursor{}, ErrInvalidCursor
	}

	c := Cursor{Sort: w.S, Order: w.O, Value: w.V, ID: w.I, Prefix: w.P}
	if c.Order == "" {
		c.Order = "asc"
	}
	if c.Order != "asc" && c.Order != "desc" {
		return Cursor{}, ErrInvalidCursor
	}
	if c.ID == "" || len(c.ID) > maxKeyLen || ValidKeyPrefix(c.Prefix) != nil {
		return Cursor{}, ErrInvalidCursor
	}
	switch v := c.Value.(type) {
	case nil, json.Number:
	case string:
		if utf8.RuneCountInString(v) > SortTextPrefix {
			return Cursor{}, ErrInvalidCursor
		}
	default:
		return Cursor{}, ErrInvalidCursor
	}

	if c.Sort == "" {
		if w.N != nil || c.Value != nil {
			return Cursor{}, ErrInvalidCursor
		}
		return c, nil
	}
	if ValidSortPath(c.Sort) != nil || c.Prefix != "" || w.N == nil || (*w.N != 0 && *w.N != 1) {
		return Cursor{}, ErrInvalidCursor
	}
	c.NoValue = *w.N == 1
	if c.NoValue == (c.Value != nil) { // a missing value must have no value, a present one must have one
		return Cursor{}, ErrInvalidCursor
	}
	return c, nil
}

// CursorMismatchError says the sort or order sent with a cursor is not the one the cursor was issued for.
type CursorMismatchError struct{ msg string }

func (e *CursorMismatchError) Error() string { return e.msg }

// CheckSort compares the sort and order parameters sent alongside a cursor with the cursor's own.
// A parameter left empty is not compared, so a cursor can be sent on its own.
func (c Cursor) CheckSort(sort, order string) error {
	have := c.Order
	if have == "" {
		have = "asc"
	}
	if (sort == "" || sort == c.Sort) && (order == "" || order == have) {
		return nil
	}
	if c.Sort == "" {
		return &CursorMismatchError{fmt.Sprintf("this cursor was issued for creation order (order=%s); start again without a cursor to change the sort", have)}
	}
	return &CursorMismatchError{fmt.Sprintf("this cursor was issued for sort=%s order=%s; start again without a cursor to change the sort", c.Sort, have)}
}
