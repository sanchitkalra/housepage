package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"housepage/internal/rules"
)

// The users and tokens routes answer errors in JSON: {"error": "<code>", "message": "<for people>"}.

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSONStatus(w, status, map[string]string{"error": code, "message": message})
}

func serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("%s-failed | err=%v", what, err)
	writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
}

// decodeBody reads a small JSON object into v, refusing unknown fields so a typo is an error and not
// silently ignored. It writes the error response itself and reports false when the body is unusable.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		var wrongType *json.UnmarshalTypeError
		switch {
		case errors.Is(err, io.EOF):
			writeError(w, http.StatusBadRequest, "invalid_json", "the request body is empty; send a JSON object")
		case errors.As(err, &tooBig):
			writeError(w, http.StatusBadRequest, "invalid_json", "the request body is too large")
		case errors.As(err, &wrongType):
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%q has the wrong type: expected %s", wrongType.Field, wrongType.Type))
		case strings.HasPrefix(err.Error(), "json: unknown field"):
			writeError(w, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), "json: "))
		default:
			writeError(w, http.StatusBadRequest, "invalid_json", "the request body is not valid JSON: "+err.Error())
		}
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_json", "unexpected data after the JSON object")
		return false
	}
	return true
}

type userOut struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	IsSuperuser bool   `json:"isSuperuser"`
	MaxSites    int    `json:"maxSites"` // the effective limit: the default when none is set
	SitesUsed   *int   `json:"sitesUsed,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

func newUserOut(u user, sitesUsed *int) userOut {
	return userOut{ID: u.ID, Name: u.Name, IsSuperuser: u.IsSuperuser, MaxSites: u.siteLimit(), SitesUsed: sitesUsed, CreatedAt: u.CreatedAt}
}

type tokenOut struct {
	Label       string `json:"label"`
	Token       string `json:"token,omitempty"` // the plaintext, only when a token has just been minted
	IsSuperuser bool   `json:"isSuperuser"`
	CreatedAt   int64  `json:"createdAt"`
	Current     *bool  `json:"current,omitempty"` // in listings: the token used for this request
}

// POST /api/v1/users: a superuser token creates a user, who comes with their first ("default") token.
func (s *server) postUser(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateJSON(w, r)
	if !ok {
		return
	}
	if !c.IsSuperuser {
		writeError(w, http.StatusForbidden, "forbidden", "a superuser token is required to create users")
		return
	}
	var req struct {
		Name      string `json:"name"`
		Superuser bool   `json:"superuser"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := rules.ValidUserName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_user_name", err.Error())
		return
	}
	u, token, err := createUser(s.db, req.Name, req.Superuser)
	switch {
	case errors.Is(err, errUserExists):
		writeError(w, http.StatusConflict, "user_exists", fmt.Sprintf("a user named %q already exists", req.Name))
		return
	case err != nil:
		serverError(w, "create-user", err)
		return
	}
	log.Printf("user-created | by=%s | user=%s | superuser=%t", c.UserName, u.Name, u.IsSuperuser)
	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"user":  newUserOut(u, nil),
		"token": tokenOut{Label: "default", Token: token, IsSuperuser: u.IsSuperuser, CreatedAt: u.CreatedAt},
	})
}

// GET /api/v1/users/{name}: a user may read themselves; a superuser token may read anyone.
func (s *server) getUser(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateJSON(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if name != c.UserName && !c.IsSuperuser {
		// the same answer whether or not the user exists, so a name cannot be probed
		writeError(w, http.StatusForbidden, "forbidden", "you can only read your own user")
		return
	}
	s.writeUser(w, name, http.StatusOK)
}

// PUT /api/v1/users/{name}: a superuser token changes a user's site limit.
func (s *server) putUser(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateJSON(w, r)
	if !ok {
		return
	}
	if !c.IsSuperuser {
		writeError(w, http.StatusForbidden, "forbidden", "a superuser token is required to change a user")
		return
	}
	var req struct {
		MaxSites json.RawMessage `json:"maxSites"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.MaxSites) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "nothing to change: send maxSites")
		return
	}
	var limit *int // nil, from a JSON null, restores the default
	if string(req.MaxSites) != "null" {
		var n int
		if err := json.Unmarshal(req.MaxSites, &n); err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "maxSites must be a whole number from 0 up, or null for the default")
			return
		}
		limit = &n
	}
	name := r.PathValue("name")
	u, err := getUserByName(s.db, name)
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no user named %q", name))
		return
	case err != nil:
		serverError(w, "get-user", err)
		return
	}
	if err := setMaxSites(s.db, u.ID, limit); err != nil {
		serverError(w, "set-max-sites", err)
		return
	}
	log.Printf("user-updated | by=%s | user=%s | max-sites=%v", c.UserName, u.Name, limit)
	s.writeUser(w, name, http.StatusOK)
}

func (s *server) writeUser(w http.ResponseWriter, name string, status int) {
	u, err := getUserByName(s.db, name)
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no user named %q", name))
		return
	case err != nil:
		serverError(w, "get-user", err)
		return
	}
	used, err := countSites(s.db, u.ID)
	if err != nil {
		serverError(w, "count-sites", err)
		return
	}
	writeJSONStatus(w, status, newUserOut(u, &used))
}

// POST /api/v1/tokens: mints a token for the caller's own user. A superuser token needs a superuser token.
func (s *server) postToken(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateJSON(w, r)
	if !ok {
		return
	}
	var req struct {
		Label     string `json:"label"`
		Superuser bool   `json:"superuser"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := rules.ValidLabel(req.Label); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_label", err.Error())
		return
	}
	if req.Superuser && !c.IsSuperuser {
		writeError(w, http.StatusForbidden, "forbidden", "a superuser token is required to mint a superuser token")
		return
	}
	token, info, err := mintTokenInfo(s.db, c.UserID, req.Label, req.Superuser)
	switch {
	case errors.Is(err, errLabelExists):
		writeError(w, http.StatusConflict, "label_exists", fmt.Sprintf("you already have a token labelled %q", req.Label))
		return
	case errors.Is(err, errNotSuperuser):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
		return
	case err != nil:
		serverError(w, "mint-token", err)
		return
	}
	log.Printf("token-minted | user=%s | label=%s | superuser=%t", c.UserName, info.Label, info.IsSuperuser)
	writeJSONStatus(w, http.StatusCreated, tokenOut{Label: info.Label, Token: token, IsSuperuser: info.IsSuperuser, CreatedAt: info.CreatedAt})
}

// GET /api/v1/tokens: the caller's own tokens, never a hash or a plaintext.
func (s *server) getTokens(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateJSON(w, r)
	if !ok {
		return
	}
	tokens, err := listTokens(s.db, c.UserID)
	if err != nil {
		serverError(w, "list-tokens", err)
		return
	}
	out := make([]tokenOut, len(tokens))
	for i, t := range tokens {
		current := t.Label == c.TokenLabel
		out[i] = tokenOut{Label: t.Label, IsSuperuser: t.IsSuperuser, CreatedAt: t.CreatedAt, Current: &current}
	}
	writeJSONStatus(w, http.StatusOK, out)
}

// DELETE /api/v1/tokens/{label}: revokes one of the caller's own tokens, including the one in use.
func (s *server) deleteToken(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateJSON(w, r)
	if !ok {
		return
	}
	label := r.PathValue("label")
	err := revokeToken(s.db, c.UserID, label)
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("you have no token labelled %q", label))
		return
	case err != nil:
		serverError(w, "revoke-token", err)
		return
	}
	log.Printf("token-revoked | user=%s | label=%s", c.UserName, label)
	w.WriteHeader(http.StatusNoContent)
}
