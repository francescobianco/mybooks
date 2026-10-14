package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDeviceLinks(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.PutCategory(Category{Name: "Sicilia"})
	store.Add(&Book{ID: "b1", Title: "I Malavoglia", Category: "sicilia", Format: "epub"})
	app := NewApp(Config{APIKey: "test-key-123456", SessionDays: 1, MaxUploadMB: 1, PageSize: 10}, store)
	h := app.Routes()
	do := func(method, path string, body string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}

	// login with "remember" -> redirect to /k/<token>/ and a cookie session too
	rec := do("POST", "/login", url.Values{"password": {"test-key-123456"}, "remember": {"1"}, "device": {"Kobo"}}.Encode(), form)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/k/") || rec.Header().Get("Set-Cookie") == "" {
		t.Fatalf("login: %d %q", rec.Code, loc)
	}
	base := strings.TrimSuffix(strings.SplitN(loc, "?", 2)[0], "/")

	// the token alone authenticates, and internal links keep the prefix
	rec = do("GET", base+"/?benvenuto=1", "", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `href="`+base+`/c/sicilia"`) || !strings.Contains(body, `href="/static/style.css"`) {
		t.Fatalf("home via token: %d\n%s", rec.Code, body)
	}
	if !strings.Contains(body, "Dispositivo ricordato") || !strings.Contains(body, base+"/forget") {
		t.Error("welcome banner or forget link missing")
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("missing Referrer-Policy")
	}
	// redirects stay inside the device prefix
	rec = do("GET", base+"/lang?l=it&next=%2Fc%2Fsicilia", "", nil)
	if got := rec.Header().Get("Location"); got != base+"/c/sicilia" {
		t.Errorf("lang redirect = %q", got)
	}
	// the token is read-only: no API writes
	rec = do("POST", base+"/api/categories", `{"name":"X"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("token wrote through the API: %d", rec.Code)
	}
	// device listing via API key never exposes hashes
	rec = do("GET", "/api/devices", "", map[string]string{"X-API-Key": "test-key-123456"})
	if !strings.Contains(rec.Body.String(), `"Kobo"`) || strings.Contains(rec.Body.String(), "hash") {
		t.Errorf("devices: %s", rec.Body.String())
	}
	if redactPath(base+"/c/x") != "/k/***/c/x" {
		t.Error("token not redacted in logs")
	}
	// revoke -> the link stops working
	do("POST", base+"/forget", "", form)
	rec = do("GET", base+"/", "", nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("revoked token still works: %d", rec.Code)
	}
	// plain cookie sessions are unchanged
	rec = do("POST", "/login", url.Values{"password": {"test-key-123456"}}.Encode(), form)
	if rec.Header().Get("Location") != "/" {
		t.Errorf("normal login redirect = %q", rec.Header().Get("Location"))
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Cookie", strings.Split(rec.Header().Get("Set-Cookie"), ";")[0])
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != 200 || strings.Contains(rec2.Body.String(), "/k/") {
		t.Errorf("cookie session: %d", rec2.Code)
	}
}
