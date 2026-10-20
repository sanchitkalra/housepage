package rules

import (
	"errors"
	"strings"
	"testing"
)

func TestValidCollection(t *testing.T) {
	good := []string{"events", "a", "my_list", "a-b", "0day", strings.Repeat("a", 64)}
	bad := []string{"", "_kv", "_x", "Events", "a b", "a.b", "-a", "a/b", "é", strings.Repeat("a", 65)}
	for _, n := range good {
		if err := ValidCollection(n); err != nil {
			t.Errorf("ValidCollection(%q) = %v, want ok", n, err)
		}
	}
	for _, n := range bad {
		if ValidCollection(n) == nil {
			t.Errorf("ValidCollection(%q) = ok, want error", n)
		}
	}
}

func TestValidKey(t *testing.T) {
	good := []string{"score", "score:amy", "a.b_c-d:1", "A", strings.Repeat("k", 128)}
	bad := []string{"", " ", "a b", "a/b", "a?b", "é", strings.Repeat("k", 129), "new\nline"}
	for _, k := range good {
		if err := ValidKey(k); err != nil {
			t.Errorf("ValidKey(%q) = %v, want ok", k, err)
		}
	}
	for _, k := range bad {
		if ValidKey(k) == nil {
			t.Errorf("ValidKey(%q) = ok, want error", k)
		}
	}
	if ValidKeyPrefix("") != nil || ValidKeyPrefix("score:") != nil {
		t.Error("an empty or ordinary prefix should be fine")
	}
	if ValidKeyPrefix("a b") == nil || ValidKeyPrefix(strings.Repeat("k", 129)) == nil {
		t.Error("a bad prefix was accepted")
	}
}

func TestValidSortPath(t *testing.T) {
	good := []string{"title", "when.start", "a_b.c1", "0", "a.0", strings.Repeat("a", 128)}
	bad := []string{"", ".a", "a.", "a..b", "a-b", "a b", "$.a", "a[0]", "a'b", `a"b`, strings.Repeat("a", 129)}
	for _, p := range good {
		if err := ValidSortPath(p); err != nil {
			t.Errorf("ValidSortPath(%q) = %v, want ok", p, err)
		}
	}
	for _, p := range bad {
		if ValidSortPath(p) == nil {
			t.Errorf("ValidSortPath(%q) = ok, want error", p)
		}
	}
	if got := JSONPath("when.start"); got != "$.when.start" {
		t.Errorf("JSONPath = %q", got)
	}
}

func TestLimitsAgainstTheCeilings(t *testing.T) {
	for name, check := range map[string]func(int) error{
		"maxCollections": CheckMaxCollections, "maxDocs": CheckMaxDocs, "maxDocSize": CheckMaxDocSize,
	} {
		if check(1) != nil {
			t.Errorf("%s: 1 should be fine", name)
		}
		if err := check(-1); err == nil || errors.As(err, new(*AboveCeilingError)) {
			t.Errorf("%s: a negative value should be a plain error, got %v", name, err)
		}
	}
	for _, n := range []int{0, 100} {
		if CheckMaxCollections(n) != nil {
			t.Errorf("maxCollections %d should be allowed", n)
		}
	}
	if CheckMaxDocs(0) != nil || CheckMaxDocs(20000) != nil {
		t.Error("maxDocs 0 and 20000 should be allowed")
	}
	if CheckMaxDocSize(1) != nil || CheckMaxDocSize(4096) != nil {
		t.Error("maxDocSize 1 and 4096 should be allowed")
	}
	if CheckMaxDocSize(0) == nil {
		t.Error("a document size of 0 would store nothing")
	}

	var above *AboveCeilingError
	err := CheckMaxDocs(20001)
	if !errors.As(err, &above) || above.Field != "maxDocs" || above.Value != 20001 || above.Ceiling != 20000 {
		t.Fatalf("CheckMaxDocs(20001) = %v", err)
	}
	if err.Error() != "maxDocs 20001 is above the server ceiling of 20000" {
		t.Errorf("message %q", err.Error())
	}
	if !errors.As(CheckMaxCollections(101), &above) || !errors.As(CheckMaxDocSize(4097), &above) {
		t.Error("the other ceilings should report AboveCeilingError too")
	}
}

func TestParseLimit(t *testing.T) {
	for in, want := range map[string]int{"": 20, "1": 1, "5": 5, "100": 100} {
		if got, err := ParseLimit(in); err != nil || got != want {
			t.Errorf("ParseLimit(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "-1", "101", "abc", "2.5", " 5", "5 "} {
		if _, err := ParseLimit(in); err == nil {
			t.Errorf("ParseLimit(%q) = ok, want error", in)
		}
	}
}

func TestParseOrder(t *testing.T) {
	for in, want := range map[string]bool{"": false, "asc": false, "desc": true} {
		if got, err := ParseOrder(in); err != nil || got != want {
			t.Errorf("ParseOrder(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"ASC", "up", "descending", " desc"} {
		if _, err := ParseOrder(in); err == nil {
			t.Errorf("ParseOrder(%q) = ok, want error", in)
		}
	}
}

func TestTheDefaultsAreInsideTheCeilings(t *testing.T) {
	if CheckMaxCollections(DefaultMaxCollections) != nil || CheckMaxDocs(DefaultMaxDocs) != nil || CheckMaxDocSize(DefaultMaxDocSize) != nil {
		t.Error("a default is outside its own ceiling")
	}
	if DefaultPageSize > MaxPageSize {
		t.Error("the default page is larger than the maximum")
	}
}
