package main

import (
	"bytes"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type cli struct {
	t      *testing.T
	db     *sql.DB
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	return &cli{t: t, db: schemaDB(t)}
}

// run executes "housepaged <group> <args...>" against the test database and returns stdout.
func (c *cli) run(group string, args ...string) (string, error) {
	c.stdout.Reset()
	c.stderr.Reset()
	env := cmdEnv{
		open:   func(string) (*sql.DB, func(), error) { return c.db, func() {}, nil },
		stdout: &c.stdout,
		stderr: &c.stderr,
	}
	err := env.run(group, args)
	return c.stdout.String(), err
}

func (c *cli) mustRun(group string, args ...string) string {
	c.t.Helper()
	out, err := c.run(group, args...)
	if err != nil {
		c.t.Fatalf("housepaged %s %v: %v", group, args, err)
	}
	return out
}

func (c *cli) mustFail(want string, group string, args ...string) {
	c.t.Helper()
	_, err := c.run(group, args...)
	if err == nil || !strings.Contains(err.Error(), want) {
		c.t.Errorf("housepaged %s %v: got %v, want an error containing %q", group, args, err, want)
	}
}

func TestUserCreateLeavesOnlyTheTokenOnStdout(t *testing.T) {
	c := newCLI(t)
	out := c.mustRun("user", "create", "sam")
	token := strings.TrimSpace(out)
	if !strings.HasPrefix(token, "hp_") || strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout should be exactly the token, got %q", out)
	}
	if !strings.Contains(c.stderr.String(), "shown only once") {
		t.Errorf("stderr: %q", c.stderr.String())
	}
	got, err := lookupToken(c.db, token)
	if err != nil || got.UserName != "sam" || got.TokenLabel != "default" || got.IsSuperuser {
		t.Errorf("the printed token resolves to %+v (%v)", got, err)
	}
}

func TestUserCreateSuperuserAndFlagPositions(t *testing.T) {
	c := newCLI(t)
	for name, args := range map[string][]string{
		"flag first":  {"create", "--superuser", "root"},
		"flag last":   {"create", "root2", "--superuser"},
		"data option": {"create", "--data", "somewhere", "root3", "--superuser"},
	} {
		token := strings.TrimSpace(c.mustRun("user", args...))
		if got, err := lookupToken(c.db, token); err != nil || !got.IsSuperuser {
			t.Errorf("%s: %+v (%v), want a superuser default token", name, got, err)
		}
	}
}

func TestUserCreateErrors(t *testing.T) {
	c := newCLI(t)
	c.mustRun("user", "create", "sam")
	c.mustFail("already exists", "user", "create", "sam")
	c.mustFail("usage", "user", "create")
	c.mustFail("usage", "user", "create", "a", "b")
	c.mustFail("invalid user name", "user", "create", "   ")
}

func TestUserSetMaxSites(t *testing.T) {
	c := newCLI(t)
	c.mustRun("user", "create", "sam")
	c.mustRun("user", "set", "sam", "--max-sites", "25")
	if u, _ := getUserByName(c.db, "sam"); u.siteLimit() != 25 {
		t.Errorf("limit %d, want 25", u.siteLimit())
	}
	c.mustRun("user", "set", "--max-sites", "default", "sam")
	if u, _ := getUserByName(c.db, "sam"); u.MaxSites != nil {
		t.Errorf("\"default\" should clear the limit, got %v", *u.MaxSites)
	}
	c.mustRun("user", "set", "sam", "--max-sites", "0")
	if u, _ := getUserByName(c.db, "sam"); u.siteLimit() != 0 {
		t.Errorf("0 is a valid limit, got %d", u.siteLimit())
	}
	c.mustFail("--max-sites must be", "user", "set", "sam", "--max-sites", "-3")
	c.mustFail("--max-sites must be", "user", "set", "sam", "--max-sites", "lots")
}

func TestUserSetSuperuserAndDemotion(t *testing.T) {
	c := newCLI(t)
	rootToken := strings.TrimSpace(c.mustRun("user", "create", "root", "--superuser"))
	adminToken := strings.TrimSpace(c.mustRun("token", "create", "--label", "admin", "--superuser", "root"))

	c.mustRun("user", "set", "root", "--no-superuser")
	if !strings.Contains(c.stderr.String(), "2 superuser token(s) were downgraded") {
		t.Errorf("stderr: %q", c.stderr.String())
	}
	for _, tok := range []string{rootToken, adminToken} {
		if got, err := lookupToken(c.db, tok); err != nil || got.IsSuperuser {
			t.Errorf("a token survived demotion as superuser: %+v (%v)", got, err)
		}
	}

	c.mustRun("user", "set", "root", "--superuser")
	if got, _ := lookupToken(c.db, adminToken); got.IsSuperuser {
		t.Error("promotion upgraded an old token")
	}
}

func TestUserSetErrors(t *testing.T) {
	c := newCLI(t)
	c.mustRun("user", "create", "sam")
	c.mustFail("nothing to change", "user", "set", "sam")
	c.mustFail("choose one", "user", "set", "sam", "--superuser", "--no-superuser")
	c.mustFail("no user named", "user", "set", "ghost", "--max-sites", "3")
	c.mustFail("usage", "user", "set", "--max-sites", "3")
}

func TestTokenCreate(t *testing.T) {
	c := newCLI(t)
	c.mustRun("user", "create", "sam")
	c.mustRun("user", "create", "root", "--superuser")

	out := c.mustRun("token", "create", "--label", "ci", "sam")
	token := strings.TrimSpace(out)
	if got, err := lookupToken(c.db, token); err != nil || got.UserName != "sam" || got.TokenLabel != "ci" || got.IsSuperuser {
		t.Errorf("%+v (%v)", got, err)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("stdout should be only the token: %q", out)
	}

	c.mustFail("already has a token labelled", "token", "create", "--label", "ci", "sam")
	c.mustFail("cannot hold a superuser token", "token", "create", "--label", "admin", "--superuser", "sam")
	c.mustFail("no user named", "token", "create", "--label", "x", "ghost")
	c.mustFail("--label is required", "token", "create", "sam") // the old syntax: <owner> alone
	c.mustFail("invalid token label", "token", "create", "--label", "bad label", "sam")
	if tok := strings.TrimSpace(c.mustRun("token", "create", "root", "--superuser", "--label", "admin")); tok == "" {
		t.Error("a superuser token for a superuser should be created")
	}
}

func TestTokenListAndRevoke(t *testing.T) {
	c := newCLI(t)
	samDefault := strings.TrimSpace(c.mustRun("user", "create", "sam"))
	c.mustRun("user", "create", "root", "--superuser")
	laptop := strings.TrimSpace(c.mustRun("token", "create", "--label", "laptop", "sam"))

	all := c.mustRun("token", "list")
	for _, want := range []string{"root\tdefault\tsuperuser\t", "sam\tdefault\tordinary\t", "sam\tlaptop\tordinary\t"} {
		if !strings.Contains(all, want) {
			t.Errorf("full listing is missing %q:\n%s", want, all)
		}
	}
	just := c.mustRun("token", "list", "sam")
	if strings.Contains(just, "root") || strings.Count(just, "\n") != 2 {
		t.Errorf("listing for sam:\n%s", just)
	}
	for _, secret := range []string{samDefault, laptop, hashToken(samDefault), hashToken(laptop)} {
		if strings.Contains(all, secret) || strings.Contains(c.stderr.String(), secret) {
			t.Error("a listing shows a token or its hash")
		}
	}

	c.mustRun("token", "revoke", "sam", "laptop")
	if _, err := lookupToken(c.db, laptop); !errors.Is(err, errUnknownToken) {
		t.Errorf("a revoked token still works: %v", err)
	}
	if _, err := lookupToken(c.db, samDefault); err != nil {
		t.Errorf("revoking laptop broke the default token: %v", err)
	}
	c.mustFail("has no token labelled", "token", "revoke", "sam", "laptop")
	c.mustFail("no user named", "token", "revoke", "ghost", "x")
	c.mustFail("usage", "token", "revoke", "sam")
	c.mustFail("no user named", "token", "list", "ghost")

	empty := newCLI(t)
	if out := empty.mustRun("token", "list"); out != "" || !strings.Contains(empty.stderr.String(), "no tokens") {
		t.Errorf("an empty listing: stdout %q stderr %q", out, empty.stderr.String())
	}
}

func TestUnknownCommands(t *testing.T) {
	c := newCLI(t)
	c.mustFail("usage", "user")
	c.mustFail("unknown command", "user", "delete")
	c.mustFail("unknown command", "token", "show")
}
