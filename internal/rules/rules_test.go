package rules

import "testing"

func TestValidName(t *testing.T) {
	good := []string{"timer", "my-notes", "abc", "a1b", "xyz"}
	bad := []string{"", "ab", "-abc", "abc-", "ABC", "a_b", "a.b", "app", "www", "xn--abc", "a b",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, n := range good {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v, want ok", n, err)
		}
	}
	for _, n := range bad {
		if ValidName(n) == nil {
			t.Errorf("ValidName(%q) = ok, want error", n)
		}
	}
}

func TestValidPath(t *testing.T) {
	good := []string{"index.html", "css/app.css", ".well-known/x", "a/b/c.js"}
	bad := []string{"", "/etc/passwd", "../x", "a/../b", "a//b", "./a", "a/", "a\\b", "a\x00b", "a/.."}
	for _, p := range good {
		if err := ValidPath(p); err != nil {
			t.Errorf("ValidPath(%q) = %v, want ok", p, err)
		}
	}
	for _, p := range bad {
		if ValidPath(p) == nil {
			t.Errorf("ValidPath(%q) = ok, want error", p)
		}
	}
}

func TestCheckFiles(t *testing.T) {
	idx := Entry{"index.html", 10}
	cases := map[string][]Entry{
		"no files":      nil,
		"no index":      {{"a.html", 1}},
		"duplicate":     {idx, idx},
		"file is dir":   {idx, {"a", 1}, {"a/b", 1}},
		"traversal":     {idx, {"../x", 1}},
		"file too big":  {idx, {"big.bin", MaxFileBytes + 1}},
		"total too big": {idx, {"a", MaxFileBytes}, {"b", MaxFileBytes}, {"c", MaxFileBytes}, {"d", MaxFileBytes}, {"e", MaxFileBytes}, {"f", MaxFileBytes}},
	}
	for name, files := range cases {
		if CheckFiles(files) == nil {
			t.Errorf("%s: want error, got ok", name)
		}
	}
	if err := CheckFiles([]Entry{idx, {"css/app.css", 5}}); err != nil {
		t.Errorf("valid site rejected: %v", err)
	}
}
