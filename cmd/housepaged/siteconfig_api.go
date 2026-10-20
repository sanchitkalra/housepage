package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"housepage/internal/rules"
)

type limitsOut struct {
	MaxCollections int `json:"maxCollections"`
	MaxDocs        int `json:"maxDocs"`
	MaxDocSize     int `json:"maxDocSize"`
}

type configOut struct {
	Site     string    `json:"site"`
	Mode     string    `json:"mode"`
	Limits   limitsOut `json:"limits"`
	Ceilings limitsOut `json:"ceilings"`
	Usage    siteUsage `json:"usage"`
}

func (s *server) mountSiteConfig(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/sites/{site}/db-config", s.asOwner(s.getDBConfig))
	mux.HandleFunc("PUT /api/v1/sites/{site}/db-config", s.asOwner(s.putDBConfig))
}

func (s *server) writeConfig(w http.ResponseWriter, site string) {
	lim, err := getSiteLimits(s.db, site)
	if err != nil {
		serverError(w, "site-settings", err)
		return
	}
	use, err := getSiteUsage(s.db, site)
	if err != nil {
		serverError(w, "site-usage", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, configOut{
		Site:     site,
		Mode:     lim.Mode,
		Limits:   limitsOut{lim.MaxCollections, lim.MaxDocs, lim.MaxDocSize},
		Ceilings: limitsOut{rules.CeilingMaxCollections, rules.CeilingMaxDocs, rules.CeilingMaxDocSize},
		Usage:    use,
	})
}

func (s *server) getDBConfig(w http.ResponseWriter, r *http.Request, site string) {
	s.writeConfig(w, site)
}

// parseLimit reads one limit of an update: absent, null (the default) or a whole number inside its bounds.
// It answers the error itself.
func parseLimit(w http.ResponseWriter, field string, raw json.RawMessage, check func(int) error) (optInt, bool) {
	if len(raw) == 0 {
		return optInt{}, true
	}
	if string(raw) == "null" {
		return optInt{set: true}, true
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", field+" must be a whole number, or null for the default")
		return optInt{}, false
	}
	if err := check(n); err != nil {
		var above *rules.AboveCeilingError
		code := "invalid_request"
		if errors.As(err, &above) {
			code = "above_ceiling"
		}
		writeError(w, http.StatusBadRequest, code, err.Error())
		return optInt{}, false
	}
	return optInt{set: true, val: &n}, true
}

// putDBConfig changes only the fields sent. A limit sent as null goes back to its default.
// Lowering a limit below current usage is accepted: it only stops new inserts.
func (s *server) putDBConfig(w http.ResponseWriter, r *http.Request, site string) {
	var req struct {
		Mode           json.RawMessage `json:"mode"`
		MaxCollections json.RawMessage `json:"maxCollections"`
		MaxDocs        json.RawMessage `json:"maxDocs"`
		MaxDocSize     json.RawMessage `json:"maxDocSize"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Mode)+len(req.MaxCollections)+len(req.MaxDocs)+len(req.MaxDocSize) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "nothing to change: send mode, maxCollections, maxDocs or maxDocSize")
		return
	}
	var p configPatch
	if len(req.Mode) > 0 {
		mode := "off" // null restores the default
		if string(req.Mode) != "null" {
			if err := json.Unmarshal(req.Mode, &mode); err != nil || (mode != "off" && mode != "open" && mode != "owner") {
				writeError(w, http.StatusBadRequest, "invalid_request", "mode must be off, open or owner")
				return
			}
		}
		p.mode = &mode
	}
	var ok bool
	if p.maxCollections, ok = parseLimit(w, "maxCollections", req.MaxCollections, rules.CheckMaxCollections); !ok {
		return
	}
	if p.maxDocs, ok = parseLimit(w, "maxDocs", req.MaxDocs, rules.CheckMaxDocs); !ok {
		return
	}
	if p.maxDocSize, ok = parseLimit(w, "maxDocSize", req.MaxDocSize, rules.CheckMaxDocSize); !ok {
		return
	}
	if err := setSiteConfig(s.db, site, p); err != nil {
		serverError(w, "set-site-config", err)
		return
	}
	log.Printf("site-config | site=%s | mode=%s | max-collections=%s | max-docs=%s | max-doc-size=%s",
		site, p.modeString(), p.maxCollections, p.maxDocs, p.maxDocSize)
	s.writeConfig(w, site)
}
