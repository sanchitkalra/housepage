package main

import (
	"net/http"
	"strings"
	"testing"
)

const settingsReply = `{"site":"tip","mode":"open",
 "limits":{"maxCollections":5,"maxDocs":1000,"maxDocSize":4096},
 "ceilings":{"maxCollections":100,"maxDocs":20000,"maxDocSize":4096},
 "usage":{"collections":2,"documentsByCollection":{"notes":3,"events":120},"kvEntries":7}}`

func answerSettings(w http.ResponseWriter, r *http.Request) { reply(w, 200, settingsReply) }

func TestDBShowPrintsSettingsAndUsage(t *testing.T) {
	_, seen := fake(t, answerSettings)
	out, _ := capture(t)
	if err := dbCmd([]string{"show", "tip"}); err != nil {
		t.Fatal(err)
	}
	got := (*seen)[0]
	if got.method != "GET" || got.path != "/api/v1/sites/tip/db-config" || got.auth != "Bearer hp_test" || got.body != "" {
		t.Errorf("request %+v", got)
	}
	want := `site         tip
mode         open
collections  2 of 5 (ceiling 100)
documents    up to 1000 per collection (ceiling 20000)
  events     120
  notes      3
kv entries   7 of 1000
doc size     up to 4096 bytes (ceiling 4096)
`
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestDBConfigSendsOnlyTheFlagsGiven(t *testing.T) {
	cases := []struct {
		args []string
		body string
	}{
		{[]string{"tip", "--mode", "open"}, `{"mode":"open"}`},
		{[]string{"tip", "--max-docs", "1000"}, `{"maxDocs":1000}`},
		{[]string{"tip", "--max-docs", "0"}, `{"maxDocs":0}`}, // an explicit zero is a value, not "not given"
		{[]string{"tip", "--max-docs", "default"}, `{"maxDocs":null}`},
		{[]string{"tip", "--max-collections=default", "--max-doc-size", "default", "--mode", "off"}, `{"maxCollections":null,"maxDocSize":null,"mode":"off"}`},
		{[]string{"tip", "--mode=owner", "--max-collections=3", "--max-doc-size=100"}, `{"maxCollections":3,"maxDocSize":100,"mode":"owner"}`},
	}
	for _, c := range cases {
		_, seen := fake(t, answerSettings)
		out, _ := capture(t)
		if err := dbCmd(append([]string{"config"}, c.args...)); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		got := (*seen)[0]
		if got.method != "PUT" || got.path != "/api/v1/sites/tip/db-config" || got.body != c.body {
			t.Errorf("%v: request %+v, want body %s", c.args, got, c.body)
		}
		if !strings.Contains(out.String(), "mode         open") {
			t.Errorf("%v: the new settings are printed: %q", c.args, out.String())
		}
	}
}

func TestDBConfigRefusesBadInputWithoutAskingTheServer(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no flags", []string{"tip"}, "nothing to change"},
		{"no site", []string{}, "usage"},
		{"flag where the site goes", []string{"--mode", "open"}, "usage"},
		{"extra argument", []string{"tip", "--mode", "open", "more"}, "usage"},
		{"bad site name", []string{"Tip", "--mode", "open"}, "invalid"},
		{"bad mode", []string{"tip", "--mode", "public"}, `invalid mode "public"`},
		{"docs above ceiling", []string{"tip", "--max-docs", "30000"}, "maxDocs 30000 is above the server ceiling of 20000"},
		{"collections above ceiling", []string{"tip", "--max-collections", "101"}, "ceiling of 100"},
		{"doc size above ceiling", []string{"tip", "--max-doc-size", "5000"}, "ceiling of 4096"},
		{"negative", []string{"tip", "--max-docs", "-1"}, "maxDocs"},
		{"doc size zero", []string{"tip", "--max-doc-size", "0"}, "maxDocSize"},
		{"not a number", []string{"tip", "--max-docs", "many"}, `--max-docs must be a whole number or default; got "many"`},
		{"empty value", []string{"tip", "--max-docs", ""}, "whole number or default"},
		{"default in the wrong case", []string{"tip", "--max-docs", "Default"}, "whole number or default"},
		{"unknown flag", []string{"tip", "--max-bananas", "1"}, "not defined"},
	}
	for _, c := range cases {
		_, seen := fake(t, answerSettings)
		capture(t)
		err := dbCmd(append([]string{"config"}, c.args...))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one containing %q", c.name, err, c.want)
		}
		if len(*seen) != 0 {
			t.Errorf("%s: %d requests sent, want none", c.name, len(*seen))
		}
	}
}

func TestDBCommandsShowTheServersMessage(t *testing.T) {
	fake(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 403, `{"error":"forbidden","message":"site \"tip\" belongs to someone else"}`)
	})
	capture(t)
	for _, args := range [][]string{{"show", "tip"}, {"config", "tip", "--mode", "open"}} {
		err := dbCmd(args)
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "belongs to someone else") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestDBCommandsRejectAReplyTheyDoNotUnderstand(t *testing.T) {
	fake(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 200, `{"hello":"world"}`) })
	out, _ := capture(t)
	if err := dbCmd([]string{"show", "tip"}); err == nil || !strings.Contains(err.Error(), "unexpected server reply") {
		t.Errorf("error %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q", out.String())
	}
}

func TestDBUsage(t *testing.T) {
	capture(t)
	for _, args := range [][]string{{}, {"nope"}, {"show"}, {"show", "a", "b"}} {
		if err := dbCmd(args); err == nil || !strings.Contains(err.Error(), "usage") && !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%v: %v", args, err)
		}
	}
}
