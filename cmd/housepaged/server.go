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
	owner, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	names, err := sitesOf(s.db, owner)
	if err != nil {
		log.Printf("list-failed | owner=%s | err=%v", owner, err)
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
	owner, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := rules.ValidName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	current, err := ownerOfSite(s.db, name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, fmt.Sprintf("no site named %q", name), http.StatusNotFound)
		return
	case err != nil:
		log.Printf("delete-lookup-failed | owner=%s | site=%s | err=%v", owner, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	case current != owner:
		http.Error(w, fmt.Sprintf("site %q belongs to someone else", name), http.StatusForbidden)
		return
	}
	if err := os.RemoveAll(filepath.Join(s.dataDir, "sites", name)); err != nil {
		log.Printf("delete-files-failed | owner=%s | site=%s | err=%v", owner, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := release(s.db, name); err != nil {
		log.Printf("release-failed | owner=%s | site=%s | err=%v", owner, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("deleted | owner=%s | site=%s", owner, name)
	writeJSON(w, map[string]string{"deleted": name})
}

func (s *server) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		owner, err := ownerOfToken(s.db, token)
		if err == nil {
			return owner, true
		}
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("token-lookup-failed | err=%v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return "", false
		}
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "missing or invalid token", http.StatusUnauthorized)
	return "", false
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
	owner, ok := s.authenticate(w, r)
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
	current, err := claim(s.db, name, owner)
	if err != nil {
		log.Printf("claim-failed | owner=%s | site=%s | err=%v", owner, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if current != owner {
		http.Error(w, fmt.Sprintf("name %q is already taken", name), http.StatusForbidden)
		return
	}
	if err := write(s.dataDir, name, files); err != nil {
		log.Printf("publish-failed | owner=%s | site=%s | err=%v", owner, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("published | owner=%s | site=%s | files=%d", owner, name, len(files))
	writeJSON(w, map[string]string{"url": s.siteURL(name)})
}
