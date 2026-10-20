package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seenRequest struct{ method, path, auth, body string }

// fake starts a server that records every request and answers with the given handler.
func fake(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, seenRequest{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(b)})
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	loginAs(t, srv.URL)
	return srv, &seen
}

// capture redirects the token commands' output into buffers for the length of a test.
func capture(t *testing.T) (out, errOut *bytes.Buffer) {
	t.Helper()
	out, errOut = &bytes.Buffer{}, &bytes.Buffer{}
	oldOut, oldErr := stdout, stderr
	stdout, stderr = out, errOut
	t.Cleanup(func() { stdout, stderr = oldOut, oldErr })
	return out, errOut
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func TestTokenCreateSendsTheLabelAndPrintsOnlyTheToken(t *testing.T) {
	_, seen := fake(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 201, `{"label":"ci","token":"hp_secret","isSuperuser":false,"createdAt":1}`)
	})
	out, errOut := capture(t)

	if err := tokenCmd([]string{"create", "--label", "ci"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hp_secret\n" {
		t.Errorf("stdout %q, want only the token", out.String())
	}
	if !strings.Contains(errOut.String(), "shown only once") {
		t.Errorf("stderr %q", errOut.String())
	}
	got := (*seen)[0]
	if got.method != "POST" || got.path != "/api/v1/tokens" || got.auth != "Bearer hp_test" ||
		!strings.Contains(got.body, `"label":"ci"`) || !strings.Contains(got.body, `"superuser":false`) {
		t.Errorf("request %+v", got)
	}
}

func TestTokenCreateAsksForASuperuserTokenOnlyWhenToldTo(t *testing.T) {
	_, seen := fake(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 201, `{"label":"admin","token":"hp_x","isSuperuser":true,"createdAt":1}`)
	})
	_, errOut := capture(t)
	if err := tokenCmd([]string{"create", "--superuser", "--label", "admin"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*seen)[0].body, `"superuser":true`) {
		t.Errorf("body %q", (*seen)[0].body)
	}
	if !strings.Contains(errOut.String(), "superuser token") {
		t.Errorf("stderr %q", errOut.String())
	}
}

func TestTokenCreateChecksTheLabelBeforeAskingTheServer(t *testing.T) {
	_, seen := fake(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 201, `{}`) })
	capture(t)
	for _, args := range [][]string{
		{"create"}, {"create", "--label", "bad label"}, {"create", "--label", "ok", "extra"}, {"create", "--label", ""},
	} {
		if err := tokenCmd(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("the client contacted the server for requests it could reject itself: %v", *seen)
	}
}

func TestTokenCreateShowsTheServersMessageNotRawJSON(t *testing.T) {
	fake(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 409, `{"error":"label_exists","message":"you already have a token labelled \"ci\""}`)
	})
	capture(t)
	err := tokenCmd([]string{"create", "--label", "ci"})
	if err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), `you already have a token labelled "ci"`) || strings.Contains(err.Error(), "label_exists") {
		t.Errorf("error %v", err)
	}

	fake(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 403, `{"error":"forbidden","message":"a superuser token is required to mint a superuser token"}`)
	})
	err = tokenCmd([]string{"create", "--label", "admin", "--superuser"})
	if err == nil || !strings.Contains(err.Error(), "a superuser token is required") {
		t.Errorf("error %v", err)
	}
}

func TestTokenListPrintsATable(t *testing.T) {
	_, seen := fake(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, `[{"label":"default","isSuperuser":true,"createdAt":0,"current":true},{"label":"ci","isSuperuser":false,"createdAt":86400000,"current":false}]`)
	})
	out, _ := capture(t)
	if err := tokenCmd([]string{"list"}); err != nil {
		t.Fatal(err)
	}
	want := "default\tsuperuser\t1970-01-01 00:00\tcurrent\nci\tordinary\t1970-01-02 00:00\t-\n"
	if out.String() != want {
		t.Errorf("stdout %q, want %q", out.String(), want)
	}
	if got := (*seen)[0]; got.method != "GET" || got.path != "/api/v1/tokens" {
		t.Errorf("request %+v", got)
	}
}

func TestTokenListWhenThereAreNone(t *testing.T) {
	fake(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 200, `[]`) })
	out, errOut := capture(t)
	if err := tokenCmd([]string{"list"}); err != nil || out.Len() != 0 || !strings.Contains(errOut.String(), "no tokens") {
		t.Errorf("stdout %q stderr %q err %v", out, errOut, err)
	}
}

func TestTokenRevoke(t *testing.T) {
	_, seen := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			reply(w, 200, `[{"label":"default","isSuperuser":false,"createdAt":0,"current":true},{"label":"ci","isSuperuser":false,"createdAt":0,"current":false}]`)
			return
		}
		w.WriteHeader(204)
	})
	_, errOut := capture(t)

	if err := tokenCmd([]string{"revoke", "ci"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "warning") || !strings.Contains(errOut.String(), `revoked "ci"`) {
		t.Errorf("stderr %q", errOut.String())
	}
	last := (*seen)[len(*seen)-1]
	if last.method != "DELETE" || last.path != "/api/v1/tokens/ci" || last.auth != "Bearer hp_test" {
		t.Errorf("last request %+v", last)
	}
}

func TestRevokingTheTokenInUseWarnsAndStillRevokes(t *testing.T) {
	_, seen := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			reply(w, 200, `[{"label":"default","isSuperuser":false,"createdAt":0,"current":true}]`)
			return
		}
		w.WriteHeader(204)
	})
	_, errOut := capture(t)
	if err := tokenCmd([]string{"revoke", "default"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "warning") || !strings.Contains(errOut.String(), "logged in with") {
		t.Errorf("no warning about revoking the token in use: %q", errOut.String())
	}
	if last := (*seen)[len(*seen)-1]; last.method != "DELETE" || last.path != "/api/v1/tokens/default" {
		t.Errorf("the revoke was not sent: %+v", last)
	}
}

func TestTokenRevokeErrors(t *testing.T) {
	fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			reply(w, 200, `[]`)
			return
		}
		reply(w, 404, `{"error":"not_found","message":"you have no token labelled \"nosuch\""}`)
	})
	capture(t)
	if err := tokenCmd([]string{"revoke", "nosuch"}); err == nil || !strings.Contains(err.Error(), "you have no token labelled") {
		t.Errorf("error %v", err)
	}
	for _, args := range [][]string{{"revoke"}, {"revoke", "a", "b"}, {"revoke", "bad label"}} {
		if err := tokenCmd(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
	if err := tokenCmd([]string{"frobnicate"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("unknown subcommand: %v", err)
	}
	if err := tokenCmd(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("no subcommand: %v", err)
	}
}

func TestRequestAcceptsAnySuccessStatus(t *testing.T) {
	for _, status := range []int{200, 201, 204} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		if _, err := request(config{Server: srv.URL, Token: "t"}, "GET", "/x", nil, ""); err != nil {
			t.Errorf("status %d treated as an error: %v", status, err)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	if _, err := request(config{Server: srv.URL, Token: "t"}, "GET", "/x", nil, ""); err == nil {
		t.Error("a 500 was treated as success")
	}
}

func TestPlainTextErrorsStillShowAsTheyAre(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `name "tip" is already taken`, 403)
	}))
	defer srv.Close()
	_, err := request(config{Server: srv.URL, Token: "t"}, "PUT", "/x", nil, "")
	if err == nil || !strings.Contains(err.Error(), `name "tip" is already taken`) {
		t.Errorf("error %v", err)
	}
}
