package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saufi-opi/lybrix/internal/store"
)

// keyCreate mirrors OpenAPI KeyCreate.
type keyCreate struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	Collections []string `json:"collections"`
	ExpiresIn   string   `json:"expires_in"`
}

// keyOut mirrors OpenAPI KeyOut.
type keyOut struct {
	ID          string     `json:"id"`
	Name        *string    `json:"name"`
	Scopes      []string   `json:"scopes"`
	Collections []string   `json:"collections"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
}

// keyCreated is KeyOut + raw_key — the raw key is shown exactly once, in
// the create response. Raw keys never appear in logs, events, or lists.
type keyCreated struct {
	keyOut
	RawKey string `json:"raw_key"`
}

func toKeyOut(k *store.ApiKey) keyOut {
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	cols := k.Collections
	return keyOut{
		ID: k.ID, Name: k.Name, Scopes: scopes, Collections: cols,
		LastUsedAt: k.LastUsedAt, ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
	}
}

// expiryDays maps ExpiresIn literals to days (keys.py _EXPIRY_DAYS).
var expiryDays = map[string]int{"1d": 1, "7d": 7, "30d": 30, "90d": 90}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var body keyCreate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.Name == "" || len(body.Name) > 200 {
		writeDetail(w, http.StatusUnprocessableEntity, "name must be 1-200 characters")
		return
	}
	if len(body.Scopes) == 0 {
		writeDetail(w, http.StatusUnprocessableEntity, "scopes must be a non-empty list")
		return
	}
	var invalid []string
	for _, sc := range body.Scopes {
		if !store.ValidScopes[sc] {
			invalid = append(invalid, sc)
		}
	}
	if len(invalid) > 0 {
		// 422 unknown scopes — keys.py parity
		writeDetail(w, http.StatusUnprocessableEntity, "unknown scopes: "+quoteList(invalid))
		return
	}
	if body.ExpiresIn == "" {
		body.ExpiresIn = "never"
	}
	var expiresAt *time.Time
	if body.ExpiresIn != "never" {
		days, ok := expiryDays[body.ExpiresIn]
		if !ok {
			writeDetail(w, http.StatusUnprocessableEntity,
				"expires_in must be one of 1d|7d|30d|90d|never")
			return
		}
		t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
		expiresAt = &t
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		writeDetail(w, http.StatusInternalServerError, "keygen failed")
		return
	}
	rawKey := "ragk_" + base64.RawURLEncoding.EncodeToString(b)
	col, err := s.deps.DB.CreateKey(r.Context(), body.Name,
		store.HashKey(rawKey), body.Scopes, body.Collections, expiresAt)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Audit event: created (keys.py writes the same row).
	_ = s.deps.DB.WriteEventPool(r.Context(), "info", "keys",
		"api key '"+body.Name+"' created (scopes: "+joinScopes(body.Scopes)+")",
		nil, nil, strPtr("created"), nil, map[string]any{
			"key_id": col.ID, "name": body.Name, "scopes": body.Scopes,
		})
	out := keyCreated{keyOut: toKeyOut(col), RawKey: rawKey}
	WriteJSON(w, http.StatusCreated, out)
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.deps.DB.ListKeys(r.Context())
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]keyOut, 0, len(keys))
	for _, k := range keys {
		out = append(out, toKeyOut(k))
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "key_id")
	if id == "" || !isUUID(id) {
		writeDetail(w, http.StatusNotFound, "key not found")
		return
	}
	k, err := s.deps.DB.RevokeKey(r.Context(), id)
	if err != nil {
		writeDetail(w, http.StatusNotFound, "key not found")
		return
	}
	if k.RevokedAt != nil && k.RevokedAt.After(time.Now().Add(-time.Minute)) {
		_ = s.deps.DB.WriteEventPool(r.Context(), "info", "keys",
			"api key '"+deref(k.Name)+"' revoked",
			nil, nil, strPtr("revoked"), nil, map[string]any{
				"key_id": k.ID, "name": deref(k.Name),
			})
	}
	WriteJSON(w, http.StatusOK, toKeyOut(k))
}

func joinScopes(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strPtr(s string) *string { return &s }

func quoteList(xs []string) string {
	out := "["
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += "'" + x + "'"
	}
	return out + "]"
}
