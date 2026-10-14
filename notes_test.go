package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNotesThread(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	store.Add(&Book{ID: "b1", Title: "Il Gattopardo", Format: "pdf"})
	app := NewApp(Config{APIKey: "test-key-123456", SessionDays: 1, PageSize: 10, MaxUploadMB: 1}, store)
	h := app.Routes()
	do := func(method, path, body, ctype string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-API-Key", "test-key-123456")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// the reader writes from the book page
	rec := do("POST", "/b/b1/notes", url.Values{"text": {"Mi converti questo libro in EPUB?"}}.Encode(), "application/x-www-form-urlencoded")
	if rec.Code != 303 || !strings.HasSuffix(rec.Header().Get("Location"), "/b/b1#note") {
		t.Fatalf("add note: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var threads []NoteThread
	json.Unmarshal(do("GET", "/api/notes?pending=1", "", "").Body.Bytes(), &threads)
	if len(threads) != 1 || !threads[0].Pending || threads[0].Notes[0].Author != NoteByUser {
		t.Fatalf("pending threads: %+v", threads)
	}
	// the agent replies through the API
	if rec := do("POST", "/api/books/b1/notes", `{"text":"Fatto: convertito in EPUB."}`, "application/json"); rec.Code != 201 {
		t.Fatalf("reply: %d %s", rec.Code, rec.Body.String())
	}
	threads = nil
	json.Unmarshal(do("GET", "/api/notes?pending=1", "", "").Body.Bytes(), &threads)
	if len(threads) != 0 {
		t.Errorf("thread still pending after reply")
	}
	page := do("GET", "/b/b1", "", "").Body.String()
	if !strings.Contains(page, "Mi converti questo libro in EPUB?") || !strings.Contains(page, "Claude ·") {
		t.Error("thread not shown on the book page")
	}
	if !strings.Contains(do("GET", "/notes", "", "").Body.String(), "Risposto") {
		t.Error("notes overview missing")
	}
}
