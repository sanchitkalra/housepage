package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"housepage/internal/rules"
)

// The data routes are mounted twice from one set of handlers:
//   - app host, /api/v1/sites/{site}/...: the site's owner, with a token, in any data mode
//   - site host, /_api/...: no token; the site comes from the X-Housepage-Site header nginx sets, and the
//     site's data mode decides what is allowed
// Every handler receives the site name already checked, and answers errors in JSON.

const siteHeader = "X-Housepage-Site"

// maxBodyBytes only bounds what is read; the real limit is the site's maxDocSize, checked on the stored text.
const maxBodyBytes = 64 << 10

type dataHandler func(w http.ResponseWriter, r *http.Request, site string)

func (s *server) mountData(mux *http.ServeMux) {
	routes := []struct {
		method, path string
		h            dataHandler
	}{
		{"GET", "/kv/{key}", s.getKV},
		{"PUT", "/kv/{key}", s.putKV},
		{"DELETE", "/kv/{key}", s.deleteKV},
		{"GET", "/kv", s.listKV},
		{"POST", "/db/{collection}", s.postDoc},
		{"GET", "/db/{collection}/{id}", s.getDocument},
		{"PUT", "/db/{collection}/{id}", s.putDoc},
		{"DELETE", "/db/{collection}/{id}", s.deleteDocument},
		{"GET", "/db/{collection}", s.listDocuments},
	}
	for _, rt := range routes {
		mux.HandleFunc(rt.method+" /api/v1/sites/{site}"+rt.path, s.asOwner(rt.h))
		mux.HandleFunc(rt.method+" /_api"+rt.path, s.asPage(rt.h))
	}
}

// asOwner lets the site's owner through, whatever the site's data mode.
func (s *server) asOwner(h dataHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.authenticateJSON(w, r)
		if !ok {
			return
		}
		site := r.PathValue("site")
		if rules.ValidName(site) != nil {
			writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no site named %q", site))
			return
		}
		ownerID, err := siteOwnerID(s.db, site)
		switch {
		case errors.Is(err, errNotFound):
			writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no site named %q", site))
			return
		case err != nil:
			serverError(w, "site-lookup", err)
			return
		case ownerID != c.UserID:
			writeError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("site %q belongs to someone else", site))
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)), site)
	}
}

// callerKey carries the authenticated caller from asOwner to a handler that wants to log who it was.
type callerKey struct{}

func callerName(r *http.Request) string {
	c, _ := r.Context().Value(callerKey{}).(caller)
	return c.UserName
}

// asPage serves a visitor's page. The site is whatever nginx says it is, and its mode decides: off allows
// nothing, owner allows reads only, open allows everything. An unclaimed name has no settings, so it is off.
func (s *server) asPage(h dataHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		site := r.Header.Get(siteHeader)
		if rules.ValidName(site) != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "missing or invalid "+siteHeader+" header")
			return
		}
		lim, err := getSiteLimits(s.db, site)
		if err != nil {
			serverError(w, "site-settings", err)
			return
		}
		switch {
		case lim.Mode == "off":
			writeError(w, http.StatusForbidden, "db_disabled", "this site's data is not enabled")
			return
		case lim.Mode == "owner" && r.Method != http.MethodGet:
			writeError(w, http.StatusForbidden, "read_only", "this site's data can only be changed by its owner")
			return
		}
		h(w, r, site)
	}
}

type docOut struct {
	ID        string          `json:"id"`
	CreatedAt int64           `json:"createdAt"`
	UpdatedAt int64           `json:"updatedAt"`
	Data      json.RawMessage `json:"data"`
}

func newDocOut(d doc) docOut { return docOut{d.ID, d.CreatedAt, d.UpdatedAt, d.Data} }

func writePage(w http.ResponseWriter, p page) {
	out := struct {
		Items      []docOut `json:"items"`
		NextCursor *string  `json:"nextCursor"`
	}{Items: make([]docOut, len(p.Items))}
	for i, d := range p.Items {
		out.Items[i] = newDocOut(d)
	}
	if p.Next != "" {
		out.NextCursor = &p.Next
	}
	writeJSONStatus(w, http.StatusOK, out)
}

// storeError answers a failure of a store write or read.
func storeError(w http.ResponseWriter, what string, err error) {
	var q *quotaError
	switch {
	case errors.Is(err, errNotObject):
		writeError(w, http.StatusBadRequest, "not_an_object", err.Error())
	case errors.Is(err, errTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "document_too_large", err.Error())
	case errors.As(err, &q):
		writeError(w, http.StatusConflict, "quota_exceeded", err.Error())
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+" not found")
	default:
		serverError(w, "data", err)
	}
}

// readJSONBody reads a request body that must be exactly one JSON value.
func readJSONBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "document_too_large", fmt.Sprintf("the request body is over %d bytes", maxBodyBytes))
		return nil, false
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_json", "the request body could not be read")
		return nil, false
	case !json.Valid(body):
		writeError(w, http.StatusBadRequest, "invalid_json", "the request body is not valid JSON")
		return nil, false
	}
	return body, true
}

// checkStrategy accepts the optional strategy parameter, whose only value in v0 is replace.
func checkStrategy(w http.ResponseWriter, r *http.Request) bool {
	if v := r.URL.Query().Get("strategy"); v != "" && v != "replace" {
		writeError(w, http.StatusBadRequest, "unknown_strategy", fmt.Sprintf("unknown strategy %q; valid strategies: replace", v))
		return false
	}
	return true
}

func badKey(w http.ResponseWriter, key string) bool {
	if err := rules.ValidKey(key); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_key", err.Error())
		return true
	}
	return false
}

func badCollection(w http.ResponseWriter, name string) bool {
	if err := rules.ValidCollection(name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_collection", err.Error())
		return true
	}
	return false
}

func (s *server) getKV(w http.ResponseWriter, r *http.Request, site string) {
	key := r.PathValue("key")
	if badKey(w, key) {
		return
	}
	d, err := getDoc(s.db, site, rules.KVCollection, key)
	if err != nil {
		storeError(w, "key", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, newDocOut(d))
}

func (s *server) putKV(w http.ResponseWriter, r *http.Request, site string) {
	key := r.PathValue("key")
	if badKey(w, key) || !checkStrategy(w, r) {
		return
	}
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	d, created, err := putKV(s.db, site, key, body)
	if err != nil {
		storeError(w, "key", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSONStatus(w, status, newDocOut(d))
}

func (s *server) deleteKV(w http.ResponseWriter, r *http.Request, site string) {
	key := r.PathValue("key")
	if badKey(w, key) {
		return
	}
	if err := deleteDoc(s.db, site, rules.KVCollection, key); err != nil {
		storeError(w, "key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listParams reads the limit and cursor that every listing takes. It answers the error itself.
func listParams(w http.ResponseWriter, r *http.Request) (limit int, cur *rules.Cursor, ok bool) {
	q := r.URL.Query()
	limit, err := rules.ParseLimit(q.Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return 0, nil, false
	}
	if q.Has("cursor") {
		c, err := rules.DecodeCursor(q.Get("cursor"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", err.Error())
			return 0, nil, false
		}
		cur = &c
	}
	return limit, cur, true
}

func (s *server) listKV(w http.ResponseWriter, r *http.Request, site string) {
	limit, cur, ok := listParams(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	prefix := q.Get("prefix")
	if err := rules.ValidKeyPrefix(prefix); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_key", err.Error())
		return
	}
	if cur != nil {
		if cur.Sort != "" {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "this cursor was not issued for a key listing")
			return
		}
		if q.Has("prefix") && prefix != cur.Prefix {
			writeError(w, http.StatusBadRequest, "cursor_mismatch",
				fmt.Sprintf("this cursor was issued for prefix=%q; start again without a cursor to change the prefix", cur.Prefix))
			return
		}
	}
	p, err := listKV(s.db, site, prefix, limit, cur)
	if err != nil {
		storeError(w, "key", err)
		return
	}
	writePage(w, p)
}

func (s *server) postDoc(w http.ResponseWriter, r *http.Request, site string) {
	collection := r.PathValue("collection")
	if badCollection(w, collection) {
		return
	}
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	d, err := insertDoc(s.db, site, collection, body)
	if err != nil {
		storeError(w, "document", err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, newDocOut(d))
}

func (s *server) getDocument(w http.ResponseWriter, r *http.Request, site string) {
	collection := r.PathValue("collection")
	if badCollection(w, collection) {
		return
	}
	d, err := getDoc(s.db, site, collection, r.PathValue("id"))
	if err != nil {
		storeError(w, "document", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, newDocOut(d))
}

func (s *server) putDoc(w http.ResponseWriter, r *http.Request, site string) {
	collection := r.PathValue("collection")
	if badCollection(w, collection) || !checkStrategy(w, r) {
		return
	}
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	d, err := replaceDoc(s.db, site, collection, r.PathValue("id"), body)
	if err != nil {
		storeError(w, "document", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, newDocOut(d))
}

func (s *server) deleteDocument(w http.ResponseWriter, r *http.Request, site string) {
	collection := r.PathValue("collection")
	if badCollection(w, collection) {
		return
	}
	if err := deleteDoc(s.db, site, collection, r.PathValue("id")); err != nil {
		storeError(w, "document", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listDocuments(w http.ResponseWriter, r *http.Request, site string) {
	collection := r.PathValue("collection")
	if badCollection(w, collection) {
		return
	}
	limit, cur, ok := listParams(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sort, order := q.Get("sort"), q.Get("order")
	if cur != nil {
		if cur.Prefix != "" {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "this cursor was not issued for a document listing")
			return
		}
		if err := cur.CheckSort(sort, order); err != nil {
			writeError(w, http.StatusBadRequest, "cursor_mismatch", err.Error())
			return
		}
	} else if q.Has("sort") {
		if err := rules.ValidSortPath(sort); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_sort", err.Error())
			return
		}
	}
	desc, err := rules.ParseOrder(order)
	if err != nil && cur == nil {
		writeError(w, http.StatusBadRequest, "invalid_order", err.Error())
		return
	}
	p, err := listDocs(s.db, site, collection, sort, desc, limit, cur)
	if err != nil {
		storeError(w, "document", err)
		return
	}
	writePage(w, p)
}
