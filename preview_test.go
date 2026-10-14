package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreviewToggle(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	store.Add(&Book{ID: "b1", Title: "I Malavoglia", Format: "epub",
		Preview: "Un tempo i Malavoglia erano stati numerosi.\n\nSecondo paragrafo."})
	app := NewApp(Config{APIKey: "test-key-123456", SessionDays: 1, PageSize: 10}, store)
	get := func(path string) string {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-API-Key", "test-key-123456")
		rec := httptest.NewRecorder()
		app.Routes().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	closed := get("/b/b1")
	if !strings.Contains(closed, "▸ Anteprima") || strings.Contains(closed, "erano stati numerosi") {
		t.Error("preview should start collapsed")
	}
	open := get("/b/b1?anteprima=1")
	if !strings.Contains(open, "▾ Anteprima") || !strings.Contains(open, "<p>Un tempo i Malavoglia erano stati numerosi.</p>") ||
		!strings.Contains(open, "<p>Secondo paragrafo.</p>") {
		t.Errorf("expanded preview missing:\n%s", open)
	}
}
