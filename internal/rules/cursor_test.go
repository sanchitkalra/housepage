package rules

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCursorRoundTrips(t *testing.T) {
	cases := map[string]Cursor{
		"creation order":      {ID: "01JBX7M9Q2F5T8W3N4K6H1R0PD", Order: "asc"},
		"creation order desc": {ID: "01JBX7M9Q2F5T8W3N4K6H1R0PD", Order: "desc"},
		"sorted by text":      {Sort: "when.start", Order: "asc", Value: "2026-03-04", ID: "01JBX7M9"},
		"sorted by number":    {Sort: "score", Order: "desc", Value: json.Number("42.5"), ID: "k1"},
		"sorted, no value":    {Sort: "when.start", Order: "asc", NoValue: true, ID: "01JBX7M9"},
		"key-value prefix":    {ID: "score:bob", Order: "asc", Prefix: "score:"},
		"text with unicode":   {Sort: "title", Order: "asc", Value: "héllo wörld 名前", ID: "x"},
	}
	for name, in := range cases {
		s := EncodeCursor(in)
		if strings.ContainsAny(s, "=+/") {
			t.Errorf("%s: %q is not plain base64url", name, s)
		}
		out, err := DecodeCursor(s)
		if err != nil || out != in {
			t.Errorf("%s: got %+v (%v), want %+v", name, out, err, in)
		}
	}
}

func TestLargeNumbersStayExact(t *testing.T) {
	in := Cursor{Sort: "n", Order: "asc", Value: json.Number("9007199254740993"), ID: "x"} // 2^53 + 1, not a float64
	out, err := DecodeCursor(EncodeCursor(in))
	if err != nil || out.Value != json.Number("9007199254740993") {
		t.Errorf("got %v (%v)", out.Value, err)
	}
}

func TestTheCursorsInTheAPIContractDecode(t *testing.T) {
	sorted, err := DecodeCursor("eyJzIjoid2hlbi5zdGFydCIsIm8iOiJhc2MiLCJuIjowLCJ2IjoiMjAyNi0wMy0wNCIsImkiOiIwMUpCWDdNOVEyRjVUOFczTjRLNkgxUjBQRCJ9")
	want := Cursor{Sort: "when.start", Order: "asc", Value: "2026-03-04", ID: "01JBX7M9Q2F5T8W3N4K6H1R0PD"}
	if err != nil || sorted != want {
		t.Errorf("sorted sample: %+v (%v)", sorted, err)
	}
	plain, err := DecodeCursor("eyJpIjoiMDFKQlg3TTlRMkY1VDhXM040SzZIMVIwUEQifQ")
	if err != nil || plain.ID != "01JBX7M9Q2F5T8W3N4K6H1R0PD" || plain.Sort != "" || plain.Order != "asc" {
		t.Errorf("creation-order sample: %+v (%v)", plain, err)
	}
	kv, err := DecodeCursor("eyJpIjoic2NvcmU6Ym9iIiwicCI6InNjb3JlOiJ9")
	if err != nil || kv.ID != "score:bob" || kv.Prefix != "score:" {
		t.Errorf("key-value sample: %+v (%v)", kv, err)
	}
	// and an ascending creation-order cursor encodes exactly like the documented sample
	if got := EncodeCursor(Cursor{ID: "01JBX7M9Q2F5T8W3N4K6H1R0PD", Order: "asc"}); got != "eyJpIjoiMDFKQlg3TTlRMkY1VDhXM040SzZIMVIwUEQifQ" {
		t.Errorf("encoded %q", got)
	}
}

func enc(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }

func TestInvalidCursorsAreRejected(t *testing.T) {
	long := strings.Repeat("a", 257)
	bad := map[string]string{
		"empty":                      "",
		"not base64":                 "!!!",
		"standard base64 characters": "eyJpIjoieCJ9+/",
		"padded":                     base64.URLEncoding.EncodeToString([]byte(`{"i":"xy"}`)), // 10 bytes: the encoding ends in ==
		"not json":                   enc("hello"),
		"not an object":              enc(`["x"]`),
		"unknown field":              enc(`{"i":"x","admin":true}`),
		"no id":                      enc(`{"s":"a","o":"asc","n":0,"v":"x"}`),
		"empty id":                   enc(`{"i":""}`),
		"id too long":                enc(`{"i":"` + strings.Repeat("a", 129) + `"}`),
		"trailing data":              enc(`{"i":"x"}{"i":"y"}`),
		"bad order":                  enc(`{"i":"x","o":"sideways"}`),
		"sort without n":             enc(`{"s":"a","o":"asc","v":"x","i":"x"}`),
		"sort with n=2":              enc(`{"s":"a","o":"asc","n":2,"v":"x","i":"x"}`),
		"bad sort path":              enc(`{"s":"a b","o":"asc","n":0,"v":"x","i":"x"}`),
		"sorted without a value":     enc(`{"s":"a","o":"asc","n":0,"i":"x"}`),
		"no-value with a value":      enc(`{"s":"a","o":"asc","n":1,"v":"x","i":"x"}`),
		"value without a sort":       enc(`{"v":"x","i":"x"}`),
		"n without a sort":           enc(`{"n":0,"i":"x"}`),
		"value is an object":         enc(`{"s":"a","o":"asc","n":0,"v":{"x":1},"i":"x"}`),
		"value is a list":            enc(`{"s":"a","o":"asc","n":0,"v":[1],"i":"x"}`),
		"value is a bool":            enc(`{"s":"a","o":"asc","n":0,"v":true,"i":"x"}`),
		"text longer than the limit": enc(`{"s":"a","o":"asc","n":0,"v":"` + long + `","i":"x"}`),
		"bad prefix":                 enc(`{"i":"x","p":"a b"}`),
		"prefix with a sort":         enc(`{"s":"a","o":"asc","n":0,"v":"x","i":"x","p":"k"}`),
		"non-canonical encoding":     "eyJpIjoieHkifR", // the canonical form of {"i":"xy"} ends in Q; R sets a spare bit
		"far too long":               strings.Repeat("A", 2049),
	}
	for name, s := range bad {
		if _, err := DecodeCursor(s); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: %v, want ErrInvalidCursor", name, err)
		}
	}
}

func TestACursorAtTheTextLimitIsAccepted(t *testing.T) {
	in := Cursor{Sort: "a", Order: "asc", Value: strings.Repeat("é", 256), ID: "x"}
	if _, err := DecodeCursor(EncodeCursor(in)); err != nil {
		t.Errorf("256 characters should be accepted: %v", err)
	}
}

func TestCheckSortComparesOnlyWhatWasSent(t *testing.T) {
	sorted := Cursor{Sort: "when.start", Order: "asc", Value: "x", ID: "i"}
	for _, ok := range [][2]string{{"", ""}, {"when.start", ""}, {"", "asc"}, {"when.start", "asc"}} {
		if err := sorted.CheckSort(ok[0], ok[1]); err != nil {
			t.Errorf("CheckSort(%q, %q) = %v, want ok", ok[0], ok[1], err)
		}
	}
	for _, bad := range [][2]string{{"title", ""}, {"", "desc"}, {"title", "asc"}, {"when.start", "desc"}} {
		err := sorted.CheckSort(bad[0], bad[1])
		var mismatch *CursorMismatchError
		if !errors.As(err, &mismatch) || !strings.Contains(err.Error(), "this cursor was issued for sort=when.start order=asc; start again without a cursor to change the sort") {
			t.Errorf("CheckSort(%q, %q) = %v", bad[0], bad[1], err)
		}
	}

	creation := Cursor{Order: "desc", ID: "i"}
	if err := creation.CheckSort("", "desc"); err != nil {
		t.Errorf("a creation-order cursor with its own order: %v", err)
	}
	if err := creation.CheckSort("title", ""); err == nil || !strings.Contains(err.Error(), "creation order (order=desc)") {
		t.Errorf("a sort sent with a creation-order cursor: %v", err)
	}
	if err := (Cursor{ID: "i"}).CheckSort("", "asc"); err != nil {
		t.Errorf("an empty order means asc: %v", err)
	}
}

func TestTheCanonicalFormOfThatCursorIsAccepted(t *testing.T) {
	if c, err := DecodeCursor("eyJpIjoieHkifQ"); err != nil || c.ID != "xy" {
		t.Errorf("%+v (%v)", c, err)
	}
}

// FuzzDecodeCursor feeds the decoder arbitrary strings. It must never panic, and any cursor it accepts
// must encode and decode back to the same cursor.
func FuzzDecodeCursor(f *testing.F) {
	for _, c := range []Cursor{
		{ID: "01JBX7M9Q2F5T8W3N4K6H1R0PD"},
		{Sort: "when.start", Order: "desc", Value: "2026-03-04", ID: "i"},
		{Sort: "n", Order: "asc", Value: json.Number("42"), ID: "i"},
		{Sort: "n", Order: "asc", NoValue: true, ID: "i"},
		{ID: "score:bob", Prefix: "score:"},
	} {
		f.Add(EncodeCursor(c))
	}
	f.Add("")
	f.Add("eyJpIjoieHkifQ")
	f.Fuzz(func(t *testing.T, s string) {
		c, err := DecodeCursor(s)
		if err != nil {
			return
		}
		again, err := DecodeCursor(EncodeCursor(c))
		if err != nil || again != c {
			t.Fatalf("%q decoded to %+v, which re-encodes to %+v (%v)", s, c, again, err)
		}
	})
}
