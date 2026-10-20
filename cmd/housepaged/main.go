package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

const usage = "usage:\n" +
	"  housepaged serve [--data DIR] [--addr ADDR] [--domain DOMAIN] [--scheme SCHEME]\n" +
	operatorUsage + "\n" +
	"  housepaged migrate status [--data DIR]"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch {
	case os.Args[1] == "serve":
		serve(os.Args[2:])
	case os.Args[1] == "user" || os.Args[1] == "token":
		env := cmdEnv{open: openForCommand, stdout: os.Stdout, stderr: os.Stderr}
		if err := env.run(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case len(os.Args) > 2 && os.Args[1] == "migrate" && os.Args[2] == "status":
		migrateStatusCommand(os.Args[3:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	data := fs.String("data", "./data", "data directory (sites/ and housepage.db)")
	addr := fs.String("addr", "127.0.0.1:8081", "listen address")
	domain := fs.String("domain", "localhost:8080", "public domain that sites are served under")
	scheme := fs.String("scheme", "http", "public URL scheme")
	fs.Parse(args)

	db, err := openDB(*data)
	if err != nil {
		log.Fatalf("open-db-failed | err=%v", err)
	}
	s := &server{db: db, dataDir: *data, scheme: *scheme, domain: *domain}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      30 * time.Second,
	}
	log.Printf("listening | addr=%s | data=%s", *addr, *data)
	log.Fatal(srv.ListenAndServe())
}

func openForCommand(dataDir string) (*sql.DB, func(), error) {
	db, err := openDB(dataDir)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { db.Close() }, nil
}

func migrateStatusCommand(args []string) {
	fs := flag.NewFlagSet("migrate status", flag.ExitOnError)
	data := fs.String("data", "./data", "data directory")
	fs.Parse(args)
	report, err := statusReport(*data)
	if err != nil {
		log.Fatalf("migrate-status-failed | err=%v", err)
	}
	fmt.Print(report)
}
