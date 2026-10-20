package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"housepage/internal/rules"
)

// Where the token commands write. Results go to stdout so a script can capture them, messages to stderr.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

const tokenUsage = `usage:
  housepage token create --label <label> [--superuser]
  housepage token list
  housepage token revoke <label>`

func tokenCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(tokenUsage)
	}
	switch args[0] {
	case "create":
		return tokenCreate(args[1:])
	case "list":
		return tokenList(args[1:])
	case "revoke":
		return tokenRevoke(args[1:])
	}
	return fmt.Errorf("unknown command %q\n%s", "token "+args[0], tokenUsage)
}

func level(superuser bool) string {
	if superuser {
		return "superuser"
	}
	return "ordinary"
}

func tokenCreate(args []string) error {
	flags := flag.NewFlagSet("token create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	label := flags.String("label", "", "a name for the token, unique among yours")
	superuser := flags.Bool("superuser", false, "mint a superuser token (needs a superuser token)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *label == "" {
		return errors.New("usage: housepage token create --label <label> [--superuser]")
	}
	if err := rules.ValidLabel(*label); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"label": *label, "superuser": *superuser})
	out, err := request(c, "POST", "/api/v1/tokens", bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	var res struct {
		Token       string
		IsSuperuser bool
	}
	if err := json.Unmarshal(out, &res); err != nil || res.Token == "" {
		return fmt.Errorf("unexpected server reply: %s", out)
	}
	fmt.Fprintf(stderr, "created %s token %q; it is shown only once\n", level(res.IsSuperuser), *label)
	fmt.Fprintln(stdout, res.Token)
	return nil
}

type tokenEntry struct {
	Label       string
	IsSuperuser bool
	CreatedAt   int64
	Current     bool
}

func fetchTokens(c config) ([]tokenEntry, error) {
	out, err := request(c, "GET", "/api/v1/tokens", nil, "")
	if err != nil {
		return nil, err
	}
	var tokens []tokenEntry
	if err := json.Unmarshal(out, &tokens); err != nil {
		return nil, fmt.Errorf("unexpected server reply: %s", out)
	}
	return tokens, nil
}

func tokenList(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: housepage token list")
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	tokens, err := fetchTokens(c)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Fprintln(stderr, "no tokens")
		return nil
	}
	for _, t := range tokens {
		inUse := "-"
		if t.Current {
			inUse = "current"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", t.Label, level(t.IsSuperuser), time.UnixMilli(t.CreatedAt).UTC().Format("2006-01-02 15:04"), inUse)
	}
	return nil
}

func tokenRevoke(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: housepage token revoke <label>")
	}
	label := args[0]
	if err := rules.ValidLabel(label); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	tokens, err := fetchTokens(c)
	if err != nil {
		return err
	}
	for _, t := range tokens {
		if t.Label == label && t.Current {
			fmt.Fprintf(stderr, "warning: %q is the token this CLI is logged in with; after revoking it, log in again with: housepage login --token <another token>\n", label)
		}
	}
	if _, err := request(c, "DELETE", "/api/v1/tokens/"+url.PathEscape(label), nil, ""); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "revoked %q; it stops working immediately\n", label)
	return nil
}
