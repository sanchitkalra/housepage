package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

var indexOnly = map[string]string{"index.html": "x"}

func TestSiteLimitStopsNewSitesButNotRepublishing(t *testing.T) {
	srv, _, tok, db := newTestServerWithDB(t)
	bob, _ := getUserByName(db, "bob")
	two := 2
	if err := setMaxSites(db, bob.ID, &two); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"aaa", "bbb"} {
		if code, body := put(t, srv, name, tok["bob"], indexOnly); code != 200 {
			t.Fatalf("%s: %d %s", name, code, body)
		}
	}
	code, body := put(t, srv, "ccc", tok["bob"], indexOnly)
	if code != 403 || !strings.Contains(body, "site limit reached: 2 of 2") {
		t.Errorf("third site: %d %q, want 403 'site limit reached: 2 of 2'", code, body)
	}
	if code, _ := put(t, srv, "aaa", tok["bob"], map[string]string{"index.html": "v2"}); code != 200 {
		t.Errorf("republishing a site you own at the limit: %d, want 200", code)
	}
	if code, _ := put(t, srv, "ccc", tok["amy"], indexOnly); code != 200 {
		t.Errorf("another user is unaffected by bob's limit: %d", code)
	}

	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/sites/aaa", tok["bob"]); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := put(t, srv, "ddd", tok["bob"], indexOnly); code != 200 {
		t.Errorf("deleting a site should free a slot: %d", code)
	}
	if code, body := put(t, srv, "eee", tok["bob"], indexOnly); code != 403 || !strings.Contains(body, "2 of 2") {
		t.Errorf("the limit is back after the slot is used: %d %q", code, body)
	}
}

func TestDefaultSiteLimitIsTen(t *testing.T) {
	srv, _, tok := newTestServer(t)
	for i := 1; i <= 10; i++ {
		if code, body := put(t, srv, fmt.Sprintf("site-%02d", i), tok["bob"], indexOnly); code != 200 {
			t.Fatalf("site %d: %d %s", i, code, body)
		}
	}
	code, body := put(t, srv, "site-11", tok["bob"], indexOnly)
	if code != 403 || !strings.Contains(body, "site limit reached: 10 of 10") {
		t.Errorf("11th site: %d %q", code, body)
	}
}

func TestLimitOfZeroAllowsNoNewSites(t *testing.T) {
	srv, _, tok, db := newTestServerWithDB(t)
	bob, _ := getUserByName(db, "bob")
	zero := 0
	setMaxSites(db, bob.ID, &zero)
	if code, body := put(t, srv, "aaa", tok["bob"], indexOnly); code != 403 || !strings.Contains(body, "0 of 0") {
		t.Errorf("%d %q", code, body)
	}
	if code, _ := put(t, srv, "aaa", tok["amy"], indexOnly); code != 200 {
		t.Errorf("a refused claim must not have taken the name: %d", code)
	}
}

func TestTakenNamesAreStillTaken(t *testing.T) {
	srv, _, tok := newTestServer(t)
	put(t, srv, "aaa", tok["bob"], indexOnly)
	if code, body := put(t, srv, "aaa", tok["amy"], indexOnly); code != 403 || !strings.Contains(body, "already taken") {
		t.Errorf("%d %q, want 403 already taken", code, body)
	}
}

func TestOwnershipFollowsTheUserNotTheToken(t *testing.T) {
	srv, _, tok, db := newTestServerWithDB(t)
	bob, _ := getUserByName(db, "bob")
	second, err := mintToken(db, bob.ID, "second", false)
	if err != nil {
		t.Fatal(err)
	}

	put(t, srv, "shared", tok["bob"], indexOnly)
	if code, _ := put(t, srv, "shared", second, map[string]string{"index.html": "v2"}); code != 200 {
		t.Errorf("a second token of the same user may republish: %d", code)
	}
	if code, body := call(t, "GET", srv.URL+"/api/v1/sites", second); code != 200 || !strings.Contains(body, "shared") {
		t.Errorf("a second token lists the user's sites: %d %s", code, body)
	}
	if code, _ := put(t, srv, "shared", tok["amy"], indexOnly); code != 403 {
		t.Errorf("another user: %d, want 403", code)
	}
}

func TestRevokedTokensStopWorkingAtOnce(t *testing.T) {
	srv, _, tok, db := newTestServerWithDB(t)
	bob, _ := getUserByName(db, "bob")
	second, _ := mintToken(db, bob.ID, "second", false)
	if code, _ := put(t, srv, "aaa", second, indexOnly); code != 200 {
		t.Fatalf("before revoking: %d", code)
	}

	if err := revokeToken(db, bob.ID, "second"); err != nil {
		t.Fatal(err)
	}
	if code, _ := put(t, srv, "aaa", second, indexOnly); code != 401 {
		t.Errorf("publish with a revoked token: %d, want 401", code)
	}
	if code, _ := call(t, "GET", srv.URL+"/api/v1/sites", second); code != 401 {
		t.Errorf("list with a revoked token: %d, want 401", code)
	}
	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/sites/aaa", second); code != 401 {
		t.Errorf("delete with a revoked token: %d, want 401", code)
	}
	if code, _ := call(t, "GET", srv.URL+"/api/v1/sites", tok["bob"]); code != 200 {
		t.Errorf("the user's other token must keep working: %d", code)
	}
}

func TestConcurrentPublishesCannotExceedTheLimit(t *testing.T) {
	srv, _, tok, db := newTestServerWithDB(t)
	bob, _ := getUserByName(db, "bob")
	three := 3
	setMaxSites(db, bob.ID, &three)

	const attempts = 8
	codes := make([]int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = put(t, srv, fmt.Sprintf("race-%d", i), tok["bob"], indexOnly)
		}()
	}
	wg.Wait()

	ok, refused := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 403:
			refused++
		}
	}
	if ok != 3 || refused != attempts-3 {
		t.Errorf("%d succeeded and %d were refused (%v), want 3 and %d", ok, refused, codes, attempts-3)
	}
	if n := count(t, db, `SELECT count(*) FROM sites WHERE owner_id = ?`, bob.ID); n != 3 {
		t.Errorf("%d sites owned, want 3", n)
	}
}

func TestDeletingASiteAlsoRemovesItsClaimAndData(t *testing.T) {
	srv, _, tok, db := newTestServerWithDB(t)
	put(t, srv, "aaa", tok["bob"], indexOnly)
	mustExec(t, db, `INSERT INTO site_db_config(site, mode) VALUES ('aaa', 'open')`)
	mustExec(t, db, `INSERT INTO docs VALUES ('aaa', '_kv', 'k', '1', 0, 0)`)

	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/sites/aaa", tok["bob"]); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if n := count(t, db, `SELECT (SELECT count(*) FROM sites) + (SELECT count(*) FROM site_db_config) + (SELECT count(*) FROM docs)`); n != 0 {
		t.Errorf("%d rows were left behind by the deleted site", n)
	}
}
