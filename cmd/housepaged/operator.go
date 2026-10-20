package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"
)

// cmdEnv is what an operator command needs: how to open the database, and where to write.
// Results (a token, a table) go to stdout so a script can capture them; messages go to stderr.
type cmdEnv struct {
	open   func(dataDir string) (*sql.DB, func(), error)
	stdout io.Writer
	stderr io.Writer
}

const operatorUsage = `  housepaged user create [--data DIR] [--superuser] <name>
  housepaged user set [--data DIR] [--max-sites N|default] [--superuser|--no-superuser] <user>
  housepaged token create [--data DIR] --label <label> [--superuser] <user>
  housepaged token list [--data DIR] [<user>]
  housepaged token revoke [--data DIR] <user> <label>`

// run dispatches "user ..." and "token ..." commands.
func (e cmdEnv) run(group string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage:\n%s", operatorUsage)
	}
	sub, rest := args[0], args[1:]
	switch group + " " + sub {
	case "user create":
		return e.userCreate(rest)
	case "user set":
		return e.userSet(rest)
	case "token create":
		return e.tokenCreate(rest)
	case "token list":
		return e.tokenList(rest)
	case "token revoke":
		return e.tokenRevoke(rest)
	}
	return fmt.Errorf("unknown command %q\nusage:\n%s", group+" "+sub, operatorUsage)
}

func (e cmdEnv) flags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs, fs.String("data", "./data", "data directory")
}

// parseInterleaved accepts flags before, between and after the positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func level(superuser bool) string {
	if superuser {
		return "superuser"
	}
	return "ordinary"
}

func (e cmdEnv) userCreate(args []string) error {
	fs, data := e.flags("user create")
	superuser := fs.Bool("superuser", false, "create a superuser")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: housepaged user create [--data DIR] [--superuser] <name>")
	}
	db, closeDB, err := e.open(*data)
	if err != nil {
		return err
	}
	defer closeDB()

	_, token, err := createUser(db, pos[0], *superuser)
	if errors.Is(err, errUserExists) {
		return fmt.Errorf("user %q already exists", pos[0])
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stderr, "created %s user %q; the default token below is shown only once\n", level(*superuser), pos[0])
	fmt.Fprintln(e.stdout, token)
	return nil
}

func (e cmdEnv) userSet(args []string) error {
	fs, data := e.flags("user set")
	maxSites := fs.String("max-sites", "", "number of sites the user may own, or \"default\"")
	superuser := fs.Bool("superuser", false, "make the user a superuser")
	noSuperuser := fs.Bool("no-superuser", false, "make the user an ordinary user (their superuser tokens become ordinary)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: housepaged user set [--data DIR] [--max-sites N|default] [--superuser|--no-superuser] <user>")
	}
	if *superuser && *noSuperuser {
		return errors.New("choose one of --superuser and --no-superuser")
	}
	if *maxSites == "" && !*superuser && !*noSuperuser {
		return errors.New("nothing to change: pass --max-sites, --superuser or --no-superuser")
	}
	var limit *int // nil restores the default
	if *maxSites != "" && *maxSites != "default" {
		n, err := strconv.Atoi(*maxSites)
		if err != nil || n < 0 {
			return fmt.Errorf("--max-sites must be a number from 0 up, or \"default\"; got %q", *maxSites)
		}
		limit = &n
	}

	db, closeDB, err := e.open(*data)
	if err != nil {
		return err
	}
	defer closeDB()
	u, err := getUserByName(db, pos[0])
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("no user named %q", pos[0])
	}
	if err != nil {
		return err
	}

	if *maxSites != "" {
		if err := setMaxSites(db, u.ID, limit); err != nil {
			return err
		}
		fmt.Fprintf(e.stderr, "%s may now own %d sites\n", u.Name, user{MaxSites: limit}.siteLimit())
	}
	if *superuser || *noSuperuser {
		var before int
		if err := db.QueryRow(`SELECT count(*) FROM tokens WHERE user_id = ? AND is_superuser = 1`, u.ID).Scan(&before); err != nil {
			return err
		}
		if err := setSuperuser(db, u.ID, *superuser); err != nil {
			return err
		}
		if *superuser {
			fmt.Fprintf(e.stderr, "%s is now a superuser; existing tokens stay ordinary, mint a superuser token with: housepaged token create --superuser\n", u.Name)
		} else {
			fmt.Fprintf(e.stderr, "%s is now an ordinary user; %d superuser token(s) were downgraded to ordinary\n", u.Name, before)
		}
	}
	return nil
}

func (e cmdEnv) tokenCreate(args []string) error {
	fs, data := e.flags("token create")
	label := fs.String("label", "", "a name for the token, unique for the user")
	superuser := fs.Bool("superuser", false, "mint a superuser token (the user must be a superuser)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *label == "" {
		return errors.New("usage: housepaged token create [--data DIR] --label <label> [--superuser] <user>\n(--label is required; a new user's first token comes from \"housepaged user create\")")
	}
	db, closeDB, err := e.open(*data)
	if err != nil {
		return err
	}
	defer closeDB()
	u, err := getUserByName(db, pos[0])
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("no user named %q; create it first with: housepaged user create %s", pos[0], pos[0])
	}
	if err != nil {
		return err
	}
	token, err := mintToken(db, u.ID, *label, *superuser)
	switch {
	case errors.Is(err, errLabelExists):
		return fmt.Errorf("%s already has a token labelled %q", u.Name, *label)
	case errors.Is(err, errNotSuperuser):
		return fmt.Errorf("%s is not a superuser, so it cannot hold a superuser token", u.Name)
	case err != nil:
		return err
	}
	fmt.Fprintf(e.stderr, "created %s token %q for %s; it is shown only once\n", level(*superuser), *label, u.Name)
	fmt.Fprintln(e.stdout, token)
	return nil
}

func (e cmdEnv) tokenList(args []string) error {
	fs, data := e.flags("token list")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New("usage: housepaged token list [--data DIR] [<user>]")
	}
	db, closeDB, err := e.open(*data)
	if err != nil {
		return err
	}
	defer closeDB()

	var rows []tokenRow
	if len(pos) == 1 {
		u, err := getUserByName(db, pos[0])
		if errors.Is(err, errNotFound) {
			return fmt.Errorf("no user named %q", pos[0])
		}
		if err != nil {
			return err
		}
		own, err := listTokens(db, u.ID)
		if err != nil {
			return err
		}
		for _, t := range own {
			rows = append(rows, tokenRow{User: u.Name, tokenInfo: t})
		}
	} else if rows, err = listAllTokens(db); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(e.stderr, "no tokens")
		return nil
	}
	for _, r := range rows {
		fmt.Fprintf(e.stdout, "%s\t%s\t%s\t%s\n", r.User, r.Label, level(r.IsSuperuser),
			time.UnixMilli(r.CreatedAt).UTC().Format("2006-01-02 15:04"))
	}
	return nil
}

func (e cmdEnv) tokenRevoke(args []string) error {
	fs, data := e.flags("token revoke")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("usage: housepaged token revoke [--data DIR] <user> <label>")
	}
	db, closeDB, err := e.open(*data)
	if err != nil {
		return err
	}
	defer closeDB()
	u, err := getUserByName(db, pos[0])
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("no user named %q", pos[0])
	}
	if err != nil {
		return err
	}
	if err := revokeToken(db, u.ID, pos[1]); errors.Is(err, errNotFound) {
		return fmt.Errorf("%s has no token labelled %q", u.Name, pos[1])
	} else if err != nil {
		return err
	}
	fmt.Fprintf(e.stderr, "revoked %q for %s; it stops working immediately\n", pos[1], u.Name)
	return nil
}
