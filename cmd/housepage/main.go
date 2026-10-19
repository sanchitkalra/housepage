package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"housepage/internal/rules"
)

const usage = `usage:
  housepage login --server URL --token TOKEN
  housepage publish <dir> --name NAME      upload a folder as NAME.<domain>; prints the live URL
  housepage list                           list your sites
  housepage delete <name>                  remove a site and free its name`

type config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func configPath() (string, error) {
	d, err := os.UserConfigDir()
	return filepath.Join(d, "housepage", "config.json"), err
}

func loadConfig() (config, error) {
	var c config
	p, err := configPath()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return c, errors.New("not logged in; run: housepage login --server URL --token TOKEN")
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func saveConfig(c config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(c)
	return os.WriteFile(p, b, 0o600)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "login":
		err = login(os.Args[2:])
	case "publish":
		err = publish(os.Args[2:])
	case "list":
		err = list()
	case "delete":
		err = del(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var httpClient = &http.Client{Timeout: 2 * time.Minute}

// request sends an authenticated request; a non-200 reply becomes an error carrying the server's message.
func request(c config, method, path string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequest(method, strings.TrimRight(c.Server, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(out))
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") { // an error page from nginx, not housepaged
			switch resp.StatusCode {
			case http.StatusTooManyRequests:
				msg = "too many requests; wait about 10 seconds and try again"
			case http.StatusRequestEntityTooLarge:
				msg = "upload too large for the server; reduce the site size"
			default:
				msg = "the server returned an unexpected error page"
			}
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, msg)
	}
	return out, nil
}

func login(args []string) error {
	flags := flag.NewFlagSet("login", flag.ExitOnError)
	server := flags.String("server", "http://localhost:8081", "housepaged URL")
	token := flags.String("token", "", "API token")
	flags.Parse(args)
	if *token == "" {
		return errors.New("--token is required")
	}
	c := config{Server: *server, Token: *token}
	if _, err := request(c, "GET", "/api/v1/sites", nil, ""); err != nil {
		return fmt.Errorf("could not log in: %w", err)
	}
	if err := saveConfig(c); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "logged in to", c.Server)
	return nil
}

type localFile struct {
	rel, abs string
	size     int64
}

func collect(dir string) ([]localFile, error) {
	var files []localFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && d.Name() == ".git" && p != dir:
			return filepath.SkipDir
		case d.IsDir() || d.Name() == ".DS_Store":
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file (symlinks are not supported)", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, localFile{filepath.ToSlash(rel), p, info.Size()})
		return nil
	})
	return files, err
}

func publish(args []string) error {
	flags := flag.NewFlagSet("publish", flag.ExitOnError)
	name := flags.String("name", "", "site name (becomes NAME.<domain>)")
	flags.Parse(args)
	dir := "."
	if flags.NArg() > 0 { // allow `publish <dir> --name x` as well as `publish --name x <dir>`
		dir = flags.Arg(0)
		flags.Parse(flags.Args()[1:])
	}
	if err := rules.ValidName(*name); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	files, err := collect(dir)
	if err != nil {
		return err
	}
	entries := make([]rules.Entry, len(files))
	for i, f := range files {
		entries[i] = rules.Entry{Path: f.rel, Size: f.size}
	}
	if err := rules.CheckFiles(entries); err != nil {
		return err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		w, err := mw.CreateFormFile("files", f.rel)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(f.abs)
		if err != nil {
			return err
		}
		w.Write(b)
	}
	if err := mw.Close(); err != nil {
		return err
	}
	out, err := request(c, "PUT", "/api/v1/sites/"+*name, &buf, mw.FormDataContentType())
	if err != nil {
		return err
	}
	var res struct{ URL string }
	if err := json.Unmarshal(out, &res); err != nil {
		return fmt.Errorf("unexpected server reply: %s", out)
	}
	fmt.Fprintf(os.Stderr, "published %d files\n", len(files))
	fmt.Println(res.URL)
	return nil
}

func list() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	out, err := request(c, "GET", "/api/v1/sites", nil, "")
	if err != nil {
		return err
	}
	var sites []struct{ Name, URL string }
	if err := json.Unmarshal(out, &sites); err != nil {
		return fmt.Errorf("unexpected server reply: %s", out)
	}
	for _, s := range sites {
		fmt.Printf("%s\t%s\n", s.Name, s.URL)
	}
	return nil
}

func del(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: housepage delete <name>")
	}
	if err := rules.ValidName(args[0]); err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if _, err := request(c, "DELETE", "/api/v1/sites/"+args[0], nil, ""); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "deleted", args[0])
	return nil
}
