package main

import (
	"errors"
	"strings"
	"testing"
)

func TestCreateUserMintsTheDefaultToken(t *testing.T) {
	db := schemaDB(t)

	sam, token, err := createUser(db, "sam", false)
	if err != nil {
		t.Fatal(err)
	}
	if sam.Name != "sam" || sam.IsSuperuser || sam.siteLimit() != 10 {
		t.Errorf("user %+v", sam)
	}
	c, err := lookupToken(db, token)
	if err != nil || c.UserID != sam.ID || c.TokenLabel != "default" || c.IsSuperuser {
		t.Errorf("the default token resolves to %+v (%v)", c, err)
	}

	root, rootToken, err := createUser(db, "root", true)
	if err != nil {
		t.Fatal(err)
	}
	c, err = lookupToken(db, rootToken)
	if err != nil || c.UserID != root.ID || !c.IsSuperuser {
		t.Errorf("a superuser account's default token should be a superuser token: %+v (%v)", c, err)
	}
}

func TestOnlyTheHashOfATokenIsStored(t *testing.T) {
	db := schemaDB(t)
	_, token, err := createUser(db, "sam", false)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM tokens WHERE hash = ?`, token); n != 0 {
		t.Error("the plaintext token is in the database")
	}
	if n := count(t, db, `SELECT count(*) FROM tokens WHERE hash = ?`, hashToken(token)); n != 1 {
		t.Error("the token's hash is not in the database")
	}
	if !strings.HasPrefix(token, "hp_") || len(token) != 3+64 {
		t.Errorf("token looks wrong: %q", token)
	}
}

func TestCreateUserRejectsBadAndDuplicateNamesWithoutLeavingRows(t *testing.T) {
	db := schemaDB(t)
	if _, _, err := createUser(db, "sam", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createUser(db, "sam", false); !errors.Is(err, errUserExists) {
		t.Errorf("duplicate: %v, want errUserExists", err)
	}
	if _, _, err := createUser(db, "   ", false); err == nil {
		t.Error("a blank name was accepted")
	}
	if users, tokens := count(t, db, `SELECT count(*) FROM users`), count(t, db, `SELECT count(*) FROM tokens`); users != 1 || tokens != 1 {
		t.Errorf("%d users and %d tokens, want 1 and 1: a failed creation left rows behind", users, tokens)
	}
}

func TestMintToken(t *testing.T) {
	db := schemaDB(t)
	sam, _, _ := createUser(db, "sam", false)
	root, _, _ := createUser(db, "root", true)

	tok, err := mintToken(db, sam.ID, "ci", false)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := lookupToken(db, tok); err != nil || c.UserID != sam.ID || c.TokenLabel != "ci" {
		t.Errorf("minted token resolves to %+v (%v)", c, err)
	}

	if _, err := mintToken(db, sam.ID, "ci", false); !errors.Is(err, errLabelExists) {
		t.Errorf("duplicate label: %v, want errLabelExists", err)
	}
	if _, err := mintToken(db, root.ID, "ci", false); err != nil {
		t.Errorf("another user may reuse a label: %v", err)
	}
	if _, err := mintToken(db, sam.ID, "bad label", false); err == nil {
		t.Error("an invalid label was accepted")
	}

	// superuser tokens: only for a superuser, who may also keep ordinary ones
	if _, err := mintToken(db, sam.ID, "admin", true); !errors.Is(err, errNotSuperuser) {
		t.Errorf("superuser token for an ordinary user: %v, want errNotSuperuser", err)
	}
	if n := count(t, db, `SELECT count(*) FROM tokens WHERE user_id = ? AND label = 'admin'`, sam.ID); n != 0 {
		t.Error("the refused superuser token was stored")
	}
	adminTok, err := mintToken(db, root.ID, "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := lookupToken(db, adminTok); !c.IsSuperuser {
		t.Error("the superuser token does not resolve as one")
	}

	if _, err := mintToken(db, 9999, "x", false); err == nil {
		t.Error("a token for a user that does not exist was accepted")
	}
}

func TestLookupTokenRejectsUnknownTokens(t *testing.T) {
	db := schemaDB(t)
	_, token, _ := createUser(db, "sam", false)
	for _, bad := range []string{"", "hp_nope", token + "x", strings.ToUpper(token)} {
		if _, err := lookupToken(db, bad); !errors.Is(err, errUnknownToken) {
			t.Errorf("lookupToken(%q) = %v, want errUnknownToken", bad, err)
		}
	}
}

func TestListTokensShowsOnlyTheUsersOwnInCreationOrder(t *testing.T) {
	db := schemaDB(t)
	sam, _, _ := createUser(db, "sam", false)
	root, _, _ := createUser(db, "root", true)
	mintToken(db, sam.ID, "laptop", false)
	mintToken(db, root.ID, "admin", true)

	got, err := listTokens(db, sam.ID)
	if err != nil || len(got) != 2 || got[0].Label != "default" || got[1].Label != "laptop" {
		t.Fatalf("sam's tokens: %+v (%v)", got, err)
	}
	if got[0].IsSuperuser || got[1].IsSuperuser {
		t.Error("an ordinary user's tokens are listed as superuser")
	}
	got, _ = listTokens(db, root.ID)
	if len(got) != 2 || !got[0].IsSuperuser || !got[1].IsSuperuser {
		t.Errorf("root's tokens: %+v", got)
	}
}

func TestRevokeToken(t *testing.T) {
	db := schemaDB(t)
	sam, samDefault, _ := createUser(db, "sam", false)
	laptop, _ := mintToken(db, sam.ID, "laptop", false)

	if err := revokeToken(db, sam.ID, "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, err := lookupToken(db, laptop); !errors.Is(err, errUnknownToken) {
		t.Errorf("a revoked token still works: %v", err)
	}
	if _, err := lookupToken(db, samDefault); err != nil {
		t.Errorf("revoking one token broke another: %v", err)
	}
	if err := revokeToken(db, sam.ID, "laptop"); !errors.Is(err, errNotFound) {
		t.Errorf("revoking twice: %v, want errNotFound", err)
	}
	if err := revokeToken(db, sam.ID, "nosuchlabel"); !errors.Is(err, errNotFound) {
		t.Errorf("unknown label: %v, want errNotFound", err)
	}
}

func TestRevokingSomeoneElsesLabelIsNotFound(t *testing.T) {
	db := schemaDB(t)
	sam, _, _ := createUser(db, "sam", false)
	_, amyDefault, _ := createUser(db, "amy", false)
	// both users have a token labelled "default"; revoking as sam must not touch amy's
	if err := revokeToken(db, sam.ID, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := lookupToken(db, amyDefault); err != nil {
		t.Errorf("revoking sam's default token revoked amy's: %v", err)
	}
	if err := revokeToken(db, sam.ID, "default"); !errors.Is(err, errNotFound) {
		t.Errorf("second revoke: %v", err)
	}
}

func TestUsersAndLimits(t *testing.T) {
	db := schemaDB(t)
	sam, _, _ := createUser(db, "sam", false)

	if _, err := getUserByName(db, "nobody"); !errors.Is(err, errNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	u, err := getUserByName(db, "sam")
	if err != nil || u.ID != sam.ID || u.MaxSites != nil || u.siteLimit() != 10 {
		t.Fatalf("%+v (%v)", u, err)
	}

	n := 25
	if err := setMaxSites(db, sam.ID, &n); err != nil {
		t.Fatal(err)
	}
	if u, _ = getUserByName(db, "sam"); u.siteLimit() != 25 {
		t.Errorf("limit %d, want 25", u.siteLimit())
	}
	if err := setMaxSites(db, sam.ID, nil); err != nil {
		t.Fatal(err)
	}
	if u, _ = getUserByName(db, "sam"); u.MaxSites != nil || u.siteLimit() != 10 {
		t.Errorf("clearing the limit should restore the default, got %+v", u)
	}
	neg := -1
	if err := setMaxSites(db, sam.ID, &neg); err == nil {
		t.Error("a negative limit was accepted")
	}
	if err := setMaxSites(db, 9999, &n); !errors.Is(err, errNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestDemotionAndPromotionOfTokens(t *testing.T) {
	db := schemaDB(t)
	root, rootDefault, _ := createUser(db, "root", true)
	adminTok, _ := mintToken(db, root.ID, "admin", true)

	if err := setSuperuser(db, root.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{rootDefault, adminTok} {
		if c, err := lookupToken(db, tok); err != nil || c.IsSuperuser {
			t.Errorf("after demotion a token still resolves as superuser: %+v (%v)", c, err)
		}
	}
	if err := setSuperuser(db, root.ID, true); err != nil {
		t.Fatal(err)
	}
	if c, _ := lookupToken(db, adminTok); c.IsSuperuser {
		t.Error("promoting a user upgraded an old token; new superuser tokens must be minted")
	}
	if _, err := mintToken(db, root.ID, "admin2", true); err != nil {
		t.Errorf("a promoted user can mint superuser tokens: %v", err)
	}
	if err := setSuperuser(db, 9999, true); !errors.Is(err, errNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestCreateUserIsAllOrNothing(t *testing.T) {
	db := schemaDB(t)
	real := newToken
	t.Cleanup(func() { newToken = real })
	newToken = func() (string, string, error) { return "hp_same", hashToken("hp_same"), nil }

	if _, _, err := createUser(db, "first", false); err != nil {
		t.Fatal(err)
	}
	// the user row is written first, then the default token collides with the first one's hash
	if _, _, err := createUser(db, "second", false); err == nil {
		t.Fatal("expected the second default token to fail")
	}
	if n := count(t, db, `SELECT count(*) FROM users WHERE name = 'second'`); n != 0 {
		t.Error("the user row stayed behind after its default token failed")
	}
	if n := count(t, db, `SELECT count(*) FROM tokens`); n != 1 {
		t.Errorf("%d tokens, want 1", n)
	}
}
