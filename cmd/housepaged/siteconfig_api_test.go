package main

import (
	"fmt"
	"strings"
	"testing"
)

func (f dataFixture) cfgGet(t *testing.T, token, site string) reply {
	t.Helper()
	return f.owner(t, token, site, "GET", "/db-config", "")
}

func (f dataFixture) cfgPut(t *testing.T, token, site, body string) reply {
	t.Helper()
	return f.owner(t, token, site, "PUT", "/db-config", body)
}

func TestDBConfigOfAnUnconfiguredSite(t *testing.T) {
	f := newDataFixture(t)
	r := f.cfgGet(t, f.bob, "bobsite")
	if r.code != 200 || at(r.obj, "site") != "bobsite" || at(r.obj, "mode") != "off" ||
		at(r.obj, "limits", "maxCollections") != float64(5) || at(r.obj, "limits", "maxDocs") != float64(500) || at(r.obj, "limits", "maxDocSize") != float64(4096) ||
		at(r.obj, "ceilings", "maxCollections") != float64(100) || at(r.obj, "ceilings", "maxDocs") != float64(20000) || at(r.obj, "ceilings", "maxDocSize") != float64(4096) ||
		at(r.obj, "usage", "collections") != float64(0) || at(r.obj, "usage", "kvEntries") != float64(0) {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, `"documentsByCollection":{}`) {
		t.Errorf("an empty usage map must be {}, not null: %s", r.raw)
	}
}

func TestDBConfigUsageCountsCollectionsAndKV(t *testing.T) {
	f := newDataFixture(t)
	f.mode(t, "bobsite", "open")
	for i := 0; i < 3; i++ {
		f.page(t, "bobsite", "POST", "/db/events", `{}`)
	}
	f.page(t, "bobsite", "POST", "/db/notes", `{}`)
	f.page(t, "bobsite", "PUT", "/kv/a", `1`)
	f.page(t, "bobsite", "PUT", "/kv/b", `1`)
	f.page(t, "amysite", "POST", "/db/other", `{}`) // another site's data is not counted
	r := f.cfgGet(t, f.bob, "bobsite")
	if at(r.obj, "usage", "collections") != float64(2) || at(r.obj, "usage", "kvEntries") != float64(2) ||
		at(r.obj, "usage", "documentsByCollection", "events") != float64(3) || at(r.obj, "usage", "documentsByCollection", "notes") != float64(1) ||
		at(r.obj, "usage", "documentsByCollection", "_kv") != nil || at(r.obj, "usage", "documentsByCollection", "other") != nil {
		t.Errorf("usage: %s", r.raw)
	}
}

func TestDBConfigPartialUpdatesChangeOnlyWhatIsSent(t *testing.T) {
	f := newDataFixture(t)
	r := f.cfgPut(t, f.bob, "bobsite", `{"mode":"open","maxDocs":1000}`)
	if r.code != 200 || at(r.obj, "mode") != "open" || at(r.obj, "limits", "maxDocs") != float64(1000) || at(r.obj, "limits", "maxCollections") != float64(5) {
		t.Fatalf("first update: %d %s", r.code, r.raw)
	}
	r = f.cfgPut(t, f.bob, "bobsite", `{"maxCollections":7}`)
	if at(r.obj, "mode") != "open" || at(r.obj, "limits", "maxDocs") != float64(1000) || at(r.obj, "limits", "maxCollections") != float64(7) {
		t.Errorf("a second update must keep the first: %s", r.raw)
	}
	r = f.cfgPut(t, f.bob, "bobsite", `{"mode":"owner"}`)
	if at(r.obj, "mode") != "owner" || at(r.obj, "limits", "maxDocs") != float64(1000) {
		t.Errorf("mode only: %s", r.raw)
	}
	if got := f.cfgGet(t, f.bob, "bobsite"); got.raw != r.raw {
		t.Errorf("GET differs from the PUT response:\n%s\n%s", got.raw, r.raw)
	}
}

func TestDBConfigNullRestoresTheDefault(t *testing.T) {
	f := newDataFixture(t)
	f.cfgPut(t, f.bob, "bobsite", `{"mode":"open","maxCollections":50,"maxDocs":9,"maxDocSize":100}`)
	r := f.cfgPut(t, f.bob, "bobsite", `{"maxDocs":null,"maxDocSize":null}`)
	if at(r.obj, "limits", "maxDocs") != float64(500) || at(r.obj, "limits", "maxDocSize") != float64(4096) ||
		at(r.obj, "limits", "maxCollections") != float64(50) || at(r.obj, "mode") != "open" {
		t.Errorf("null on two limits: %s", r.raw)
	}
	if r := f.cfgPut(t, f.bob, "bobsite", `{"mode":null}`); at(r.obj, "mode") != "off" {
		t.Errorf("null mode is the default, off: %s", r.raw)
	}
}

func TestDBConfigTakesEffectOnTheDataRoutes(t *testing.T) {
	f := newDataFixture(t)
	if r := f.page(t, "bobsite", "POST", "/db/c", `{}`); !isJSONError(r, 403, "db_disabled") {
		t.Fatalf("before: %d %s", r.code, r.raw)
	}
	f.cfgPut(t, f.bob, "bobsite", `{"mode":"open","maxDocs":1}`)
	if r := f.page(t, "bobsite", "POST", "/db/c", `{}`); r.code != 201 {
		t.Fatalf("opened: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "POST", "/db/c", `{}`); !isJSONError(r, 409, "quota_exceeded") {
		t.Errorf("one document allowed: %d %s", r.code, r.raw)
	}
}

func TestDBConfigCeilings(t *testing.T) {
	f := newDataFixture(t)
	for _, c := range []struct{ body, want string }{
		{`{"maxDocs":20001}`, "maxDocs 20001 is above the server ceiling of 20000"},
		{`{"maxCollections":101}`, "maxCollections 101 is above the server ceiling of 100"},
		{`{"maxDocSize":4097}`, "maxDocSize 4097 is above the server ceiling of 4096"},
	} {
		r := f.cfgPut(t, f.bob, "bobsite", c.body)
		if !isJSONError(r, 400, "above_ceiling") || !strings.Contains(r.raw, c.want) {
			t.Errorf("%s: %d %s", c.body, r.code, r.raw)
		}
	}
	for _, body := range []string{`{"maxDocs":20000}`, `{"maxCollections":100}`, `{"maxDocSize":4096}`, `{"maxDocs":0}`} {
		if r := f.cfgPut(t, f.bob, "bobsite", body); r.code != 200 {
			t.Errorf("%s is inside the ceiling: %d %s", body, r.code, r.raw)
		}
	}
}

func TestDBConfigRefusesBadInputAndChangesNothing(t *testing.T) {
	f := newDataFixture(t)
	f.cfgPut(t, f.bob, "bobsite", `{"mode":"open","maxDocs":10}`)
	before := f.cfgGet(t, f.bob, "bobsite").raw
	for _, body := range []string{
		``, `{}`, `[]`, `{nope`, `{"mode":"public"}`, `{"mode":5}`, `{"mode":""}`,
		`{"maxDocs":-1}`, `{"maxDocs":1.5}`, `{"maxDocs":"10"}`, `{"maxDocSize":0}`, `{"maxCollections":true}`,
		`{"unknown":1}`, `{"mode":"owner","maxDocs":-1}`, `{"mode":"owner","maxDocs":99999}`,
	} {
		r := f.cfgPut(t, f.bob, "bobsite", body)
		if r.code != 400 || (r.errCode() != "invalid_request" && r.errCode() != "invalid_json" && r.errCode() != "above_ceiling") {
			t.Errorf("%q: %d %s, want a 400", body, r.code, r.raw)
		}
	}
	if after := f.cfgGet(t, f.bob, "bobsite").raw; after != before {
		t.Errorf("a refused update changed the settings:\n%s\n%s", before, after)
	}
}

func TestLoweringALimitBelowUsageIsAccepted(t *testing.T) {
	f := newDataFixture(t)
	f.cfgPut(t, f.bob, "bobsite", `{"mode":"open"}`)
	for i := 0; i < 3; i++ {
		f.page(t, "bobsite", "POST", "/db/c", `{}`)
	}
	if r := f.cfgPut(t, f.bob, "bobsite", `{"maxDocs":1}`); r.code != 200 || at(r.obj, "usage", "documentsByCollection", "c") != float64(3) {
		t.Fatalf("lowering: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "POST", "/db/c", `{}`); !isJSONError(r, 409, "quota_exceeded") {
		t.Errorf("new insert: %d %s", r.code, r.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/db/c", ""); len(itemIDs(r)) != 3 {
		t.Errorf("existing data stays readable: %s", r.raw)
	}
}

func TestDBConfigIsOwnerOnly(t *testing.T) {
	f := newDataFixture(t)
	for _, method := range []string{"GET", "PUT"} {
		body := ""
		if method == "PUT" {
			body = `{"mode":"open"}`
		}
		for _, token := range []string{"", "hp_nope"} {
			if r := f.owner(t, token, "bobsite", method, "/db-config", body); !isJSONError(r, 401, "unauthorized") {
				t.Errorf("%s token %q: %d %s", method, token, r.code, r.raw)
			}
		}
		for name, token := range map[string]string{"amy": f.amy, "root": f.root} {
			if r := f.owner(t, token, "bobsite", method, "/db-config", body); !isJSONError(r, 403, "forbidden") {
				t.Errorf("%s %s: %d %s", method, name, r.code, r.raw)
			}
		}
		if r := f.owner(t, f.bob, "nosuchsite", method, "/db-config", body); !isJSONError(r, 404, "not_found") {
			t.Errorf("%s unknown site: %d %s", method, r.code, r.raw)
		}
	}
	if got := f.cfgGet(t, f.bob, "bobsite"); at(got.obj, "mode") != "off" {
		t.Errorf("someone else's PUT changed the site: %s", got.raw)
	}
	if r := f.page(t, "bobsite", "GET", "/db-config", ""); r.code != 404 && r.code != 405 {
		t.Errorf("the settings must not exist on the site host: %d %s", r.code, r.raw)
	}
}

func TestDBConfigRowsAreRemovedWithTheSite(t *testing.T) {
	f := newDataFixture(t)
	f.cfgPut(t, f.bob, "bobsite", `{"mode":"open"}`)
	f.page(t, "bobsite", "PUT", "/kv/a", `1`)
	if r := api(t, f.srv, "DELETE", "/api/v1/sites/bobsite", f.bob, ""); r.code != 200 {
		t.Fatalf("delete site: %d %s", r.code, r.raw)
	}
	if n := count(t, f.db, `SELECT count(*) FROM site_db_config WHERE site = 'bobsite'`) + count(t, f.db, `SELECT count(*) FROM docs WHERE site = 'bobsite'`); n != 0 {
		t.Errorf("%d rows left behind", n)
	}
}

func TestSetSiteConfigIsOneStep(t *testing.T) {
	db := docsDB(t)
	if err := setSiteConfig(db, "s", configPatch{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := getSiteLimits(db, "s"); got != (siteLimits{"off", 5, 500, 4096}) {
		t.Errorf("an empty patch creates a default row: %+v", got)
	}
	mode := "open"
	three := 3
	if err := setSiteConfig(db, "s", configPatch{mode: &mode, maxDocs: optInt{set: true, val: &three}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := getSiteLimits(db, "s"); got != (siteLimits{"open", 5, 3, 4096}) {
		t.Errorf("%+v", got)
	}
	if err := setSiteConfig(db, "nosuchsite", configPatch{mode: &mode}); err == nil {
		t.Error("a site that does not exist must fail (foreign key)")
	} else if n := count(t, db, `SELECT count(*) FROM site_db_config`); n != 1 {
		t.Errorf("%d rows after a failed insert, want 1 (%v)", n, fmt.Sprint(err))
	}
}
