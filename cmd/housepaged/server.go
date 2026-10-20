package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"housepage/internal/rules"
)

type server struct {
	db      *sql.DB
	dataDir string
	scheme  string
	domain  string
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/sites/{name}", s.putSite)
	mux.HandleFunc("GET /api/v1/sites", s.listSites)
	mux.HandleFunc("DELETE /api/v1/sites/{name}", s.deleteSite)
	mux.HandleFunc("POST /api/v1/users", s.postUser)
	mux.HandleFunc("GET /api/v1/users/{name}", s.getUser)
	mux.HandleFunc("PUT /api/v1/users/{name}", s.putUser)
	mux.HandleFunc("POST /api/v1/tokens", s.postToken)
	mux.HandleFunc("GET /api/v1/tokens", s.getTokens)
	mux.HandleFunc("DELETE /api/v1/tokens/{label}", s.deleteToken)
	return mux
}

func (s *server) siteURL(name string) string {
	return fmt.Sprintf("%s://%s.%s/", s.scheme, name, s.domain)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *server) listSites(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	names, err := sitesOfUser(s.db, c.UserID)
	if err != nil {
		log.Printf("list-failed | user=%s | err=%v", c.UserName, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type info struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	out := make([]info, len(names))
	for i, n := range names {
		out[i] = info{n, s.siteURL(n)}
	}
	writeJSON(w, out)
}

func (s *server) deleteSite(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := rules.ValidName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ownerID, err := siteOwnerID(s.db, name)
	switch {
	case errors.Is(err, errNotFound):
		http.Error(w, fmt.Sprintf("no site named %q", name), http.StatusNotFound)
		return
	case err != nil:
		log.Printf("delete-lookup-failed | user=%s | site=%s | err=%v", c.UserName, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	case ownerID != c.UserID:
		http.Error(w, fmt.Sprintf("site %q belongs to someone else", name), http.StatusForbidden)
		return
	}
	if err := os.RemoveAll(filepath.Join(s.dataDir, "sites", name)); err != nil {
		log.Printf("delete-files-failed | user=%s | site=%s | err=%v", c.UserName, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := releaseSite(s.db, name); err != nil {
		log.Printf("release-failed | user=%s | site=%s | err=%v", c.UserName, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("deleted | user=%s | site=%s", c.UserName, name)
	writeJSON(w, map[string]string{"deleted": name})
}

// callerFor identifies who a request's bearer token belongs to. A missing or unknown token is errUnknownToken.
func (s *server) callerFor(r *http.Request) (caller, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return caller{}, errUnknownToken
	}
	return lookupToken(s.db, token)
}

// authenticate answers a failure in plain text, as the publishing routes always have.
func (s *server) authenticate(w http.ResponseWriter, r *http.Request) (caller, bool) {
	c, err := s.callerFor(r)
	switch {
	case err == nil:
		return c, true
	case !errors.Is(err, errUnknownToken):
		log.Printf("token-lookup-failed | err=%v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return caller{}, false
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "missing or invalid token", http.StatusUnauthorized)
	return caller{}, false
}

// authenticateJSON answers a failure with a JSON error, for the newer routes.
func (s *server) authenticateJSON(w http.ResponseWriter, r *http.Request) (caller, bool) {
	c, err := s.callerFor(r)
	switch {
	case err == nil:
		return c, true
	case !errors.Is(err, errUnknownToken):
		serverError(w, "token-lookup", err)
		return caller{}, false
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
	return caller{}, false
}

// readFiles streams the multipart body. Each part's filename is the file's relative path;
// Go's Part.FileName() strips directories, so the header is parsed directly.
func readFiles(w http.ResponseWriter, r *http.Request) ([]file, error) {
	r.Body = http.MaxBytesReader(w, r.Body, rules.MaxTotalBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, errors.New("expected a multipart/form-data body")
	}
	var files []file
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil || params["filename"] == "" {
			return nil, fmt.Errorf("part %q has no filename; send each file as -F 'files=@local;filename=relative/path'", part.FormName())
		}
		if len(files) >= rules.MaxFiles {
			return nil, fmt.Errorf("too many files; the limit is %d", rules.MaxFiles)
		}
		data, err := io.ReadAll(io.LimitReader(part, rules.MaxFileBytes+1))
		if err != nil {
			return nil, err
		}
		files = append(files, file{path: params["filename"], data: data})
	}
}

// ponytail: the body is read before the name's owner is checked, bounded by the size limits.
func (s *server) putSite(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	files, err := readFiles(w, r)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		http.Error(w, fmt.Sprintf("upload too large; the limit is %d bytes", rules.MaxTotalBytes), http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := check(name, files); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	claimed, err := claimSite(s.db, name, c.UserID)
	if err != nil {
		log.Printf("claim-failed | user=%s | site=%s | err=%v", c.UserName, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch claimed.outcome {
	case takenByOther:
		http.Error(w, fmt.Sprintf("name %q is already taken", name), http.StatusForbidden)
		return
	case atSiteLimit:
		http.Error(w, fmt.Sprintf("site limit reached: %d of %d", claimed.used, claimed.limit), http.StatusForbidden)
		return
	}
	if err := write(s.dataDir, name, files); err != nil {
		log.Printf("publish-failed | user=%s | site=%s | err=%v", c.UserName, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("published | user=%s | site=%s | files=%d", c.UserName, name, len(files))
	writeJSON(w, map[string]string{"url": s.siteURL(name)})
}
