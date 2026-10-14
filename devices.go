package main

// Device links: browsers that drop cookies when closed (the Kobo) can stay
// logged in through a per-device token carried in the URL path:
//
//	/k/<token>/c/scacchi  ==  /c/scacchi  for a logged-in browser
//
// Tokens are random, stored only as SHA-256 hashes, read-only (the API key is
// still required to change the library) and individually revocable.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"token_hash"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

// DeviceInfo is what the API exposes (never the hash).
type DeviceInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte("mybooks-device:" + token))
	return hex.EncodeToString(h[:])
}

// CreateDevice registers a device and returns its secret token (shown once).
func (s *Store) CreateDevice(name string) (DeviceInfo, string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return DeviceInfo{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Dispositivo"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	d := Device{ID: newID(), Name: name, TokenHash: hashToken(token), CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lib.Devices = append(s.lib.Devices, d)
	return d.info(), token, s.save()
}

func (d Device) info() DeviceInfo {
	return DeviceInfo{ID: d.ID, Name: d.Name, CreatedAt: d.CreatedAt, LastUsed: d.LastUsed}
}

// DeviceByToken returns the device owning token and records its use
// (persisted at most once an hour to avoid rewriting library.json per request).
func (s *Store) DeviceByToken(token string) (DeviceInfo, bool) {
	if len(token) < 20 {
		return DeviceInfo{}, false
	}
	h := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.lib.Devices {
		d := &s.lib.Devices[i]
		if d.TokenHash == h {
			if time.Since(d.LastUsed) > time.Hour {
				d.LastUsed = time.Now().UTC()
				s.save()
			}
			return d.info(), true
		}
	}
	return DeviceInfo{}, false
}

func (s *Store) Devices() []DeviceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeviceInfo, 0, len(s.lib.Devices))
	for _, d := range s.lib.Devices {
		out = append(out, d.info())
	}
	return out
}

func (s *Store) DeleteDevice(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.lib.Devices {
		if d.ID == id {
			s.lib.Devices = append(s.lib.Devices[:i], s.lib.Devices[i+1:]...)
			return s.save()
		}
	}
	return ErrNotFound
}

// ---- request plumbing ----

type deviceCtxKey struct{}

type deviceCtx struct {
	device DeviceInfo
	base   string // "/k/<token>"
}

func deviceFrom(r *http.Request) (deviceCtx, bool) {
	d, ok := r.Context().Value(deviceCtxKey{}).(deviceCtx)
	return d, ok
}

// base is the path prefix to keep in links: "" for cookie sessions.
func base(r *http.Request) string {
	if d, ok := deviceFrom(r); ok {
		return d.base
	}
	return ""
}

// withDevices strips a valid /k/<token> prefix and marks the request as
// authenticated by that device; an unknown token sends the user to login.
func (a *App) withDevices(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/k/") {
			next.ServeHTTP(w, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/k/")
		token, path, _ := strings.Cut(rest, "/")
		d, ok := a.store.DeviceByToken(token)
		if !ok {
			http.Redirect(w, r, "/login?revocato=1", http.StatusSeeOther)
			return
		}
		r2 := r.WithContext(context.WithValue(r.Context(), deviceCtxKey{}, deviceCtx{device: d, base: "/k/" + token}))
		u := *r.URL
		u.Path, u.RawPath = "/"+path, ""
		r2.URL = &u
		next.ServeHTTP(w, r2)
	})
}

// redirect keeps the device prefix when sending a browser elsewhere in the UI.
func redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, base(r)+path, http.StatusSeeOther)
}

// internal links/forms/images
var localRefRe = regexp.MustCompile(`(href|src|action)="/([^"]*)"`)

// withBase prefixes internal URLs with the device path, leaving public
// static files (stylesheet, icons) untouched.
func withBase(html []byte, base string) []byte {
	if base == "" {
		return html
	}
	return localRefRe.ReplaceAllFunc(html, func(m []byte) []byte {
		sub := localRefRe.FindSubmatch(m)
		p := string(sub[2])
		if strings.HasPrefix(p, "static/") || strings.HasPrefix(p, "favicon") || strings.HasPrefix(p, "apple-touch") {
			return m
		}
		return []byte(string(sub[1]) + `="` + base + "/" + p + `"`)
	})
}

var tokenInPathRe = regexp.MustCompile(`^/k/[^/]+`)

// redactPath hides device tokens in logs.
func redactPath(p string) string {
	return tokenInPathRe.ReplaceAllString(p, "/k/***")
}

// ---- pages ----

// forgetPage asks for confirmation before revoking the current device link.
func (a *App) forgetPage(w http.ResponseWriter, r *http.Request) {
	d, ok := deviceFrom(r)
	if !ok {
		http.Redirect(w, r, "/logout", http.StatusSeeOther)
		return
	}
	a.page(w, r, "forget.html", map[string]any{"Title": "Disconnetti", "Device": d.device})
}

func (a *App) forgetSubmit(w http.ResponseWriter, r *http.Request) {
	if d, ok := deviceFrom(r); ok {
		a.store.DeleteDevice(d.device.ID)
	}
	clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// deviceURL is the absolute link to save as the Kobo home page.
func deviceURL(r *http.Request, token string) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/k/" + token + "/"
}

// ---- API (API key only) ----

func (a *App) apiListDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.store.Devices())
}

func (a *App) apiCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	d, token, err := a.store.CreateDevice(req.Name)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"device": d, "token": token, "url": deviceURL(r, token)})
}

func (a *App) apiDeleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteDevice(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": r.PathValue("id")})
}
