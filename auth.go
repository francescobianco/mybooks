package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "mybooks_session"

// The session signing key is derived from the API key, so rotating the key
// logs every device out.
func (a *App) sessionKey() []byte {
	h := sha256.Sum256([]byte("mybooks-session:" + a.cfg.APIKey))
	return h[:]
}

func (a *App) signSession(expiry int64) string {
	mac := hmac.New(sha256.New, a.sessionKey())
	mac.Write([]byte(strconv.FormatInt(expiry, 10)))
	return strconv.FormatInt(expiry, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *App) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, _, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(a.signSession(expiry)))
}

func (a *App) checkKey(key string) bool {
	return subtle.ConstantTimeCompare([]byte(key), []byte(a.cfg.APIKey)) == 1
}

func (a *App) validAPIKey(r *http.Request) bool {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return a.checkKey(k)
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return a.checkKey(strings.TrimPrefix(auth, "Bearer "))
	}
	return false
}

func (a *App) authenticated(r *http.Request) bool {
	if _, ok := deviceFrom(r); ok {
		return true
	}
	return a.validAPIKey(r) || a.validSession(r)
}

func (a *App) setSession(w http.ResponseWriter, r *http.Request) {
	expiry := time.Now().Add(time.Duration(a.cfg.SessionDays) * 24 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.signSession(expiry.Unix()),
		Path:     "/",
		Expires:  expiry,
		MaxAge:   int(time.Until(expiry).Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
}

// requireWeb redirects unauthenticated browsers to the login page.
func (a *App) requireWeb(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// requireAPI answers 401 JSON to unauthenticated API calls.
func (a *App) requireAPI(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		h(w, r)
	}
}
