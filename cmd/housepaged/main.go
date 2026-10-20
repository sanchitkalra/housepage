package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

const usage = `usage:
  housepaged serve [--data DIR] [--addr ADDR] [--domain DOMAIN] [--scheme SCHEME]
  housepaged token create [--data DIR] <owner>
  housepaged migrate status [--data DIR]`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch {
	case os.Args[1] == "serve":
		serve(os.Args[2:])
	case len(os.Args) > 2 && os.Args[1] == "token" && os.Args[2] == "create":
		tokenCreate(os.Args[3:])
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

func tokenCreate(args []string) {
	fs := flag.NewFlagSet("token create", flag.ExitOnError)
	data := fs.String("data", "./data", "data directory")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	db, err := openDB(*data)
	if err != nil {
		log.Fatalf("open-db-failed | err=%v", err)
	}
	token, err := createToken(db, fs.Arg(0))
	if err != nil {
		log.Fatalf("create-token-failed | err=%v", err)
	}
	fmt.Println(token)
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
