package main

// Notes: a per-book thread between the reader and the coding agent. The reader
// writes requests from the book page ("convertilo in EPUB"); at the next
// development session the agent fetches pending threads through the API,
// does the work and replies in the same thread.

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	NoteByUser   = "utente"
	NoteByClaude = "claude"
	maxNoteLen   = 4000
)

type Note struct {
	ID        string    `json:"id"`
	BookID    string    `json:"book_id"`
	Author    string    `json:"author"` // "utente" or "claude"
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// NoteThread is a book with its notes, oldest first.
type NoteThread struct {
	BookID  string `json:"book_id"`
	Title   string `json:"title"`
	Author  string `json:"author,omitempty"`
	Pending bool   `json:"pending"` // last note is from the reader
	Notes   []Note `json:"notes"`
}

func (s *Store) AddNote(bookID, author, text string) (Note, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return Note{}, errors.New("empty note")
	}
	if len([]rune(text)) > maxNoteLen {
		return Note{}, errors.New("note too long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, b := s.find(bookID); b == nil {
		return Note{}, ErrNotFound
	}
	n := Note{ID: newID(), BookID: bookID, Author: author, Text: text, CreatedAt: time.Now().UTC()}
	s.lib.Notes = append(s.lib.Notes, n)
	return n, s.save()
}

func (s *Store) DeleteNote(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.lib.Notes {
		if n.ID == id {
			s.lib.Notes = append(s.lib.Notes[:i], s.lib.Notes[i+1:]...)
			return s.save()
		}
	}
	return ErrNotFound
}

func (s *Store) BookNotes(bookID string) []Note {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Note
	for _, n := range s.lib.Notes {
		if n.BookID == bookID {
			out = append(out, n)
		}
	}
	return out
}

// Threads groups notes by book; pending threads (awaiting a reply) first,
// then most recently active.
func (s *Store) Threads(pendingOnly bool) []NoteThread {
	s.mu.RLock()
	defer s.mu.RUnlock()
	by := map[string]*NoteThread{}
	var order []string
	for _, n := range s.lib.Notes {
		t, ok := by[n.BookID]
		if !ok {
			_, b := s.find(n.BookID)
			if b == nil {
				continue
			}
			t = &NoteThread{BookID: b.ID, Title: b.Title, Author: b.Author}
			by[n.BookID] = t
			order = append(order, n.BookID)
		}
		t.Notes = append(t.Notes, n)
	}
	out := make([]NoteThread, 0, len(order))
	for _, id := range order {
		t := by[id]
		t.Pending = t.Notes[len(t.Notes)-1].Author != NoteByClaude
		if !pendingOnly || t.Pending {
			out = append(out, *t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pending != out[j].Pending {
			return out[i].Pending
		}
		return out[i].Notes[len(out[i].Notes)-1].CreatedAt.After(out[j].Notes[len(out[j].Notes)-1].CreatedAt)
	})
	return out
}

// ---- web ----

func (a *App) addNoteWeb(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	id := r.PathValue("id")
	if _, err := a.store.AddNote(id, NoteByUser, r.PostFormValue("text")); err != nil && !strings.Contains(err.Error(), "empty") {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	redirect(w, r, "/b/"+id+"#note")
}

func (a *App) notesPage(w http.ResponseWriter, r *http.Request) {
	a.page(w, r, "notes.html", map[string]any{"Title": "Note", "Threads": a.store.Threads(false)})
}

// ---- API ----

// apiListNotes: GET /api/notes[?pending=1] — threads grouped by book.
func (a *App) apiListNotes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.store.Threads(r.URL.Query().Get("pending") == "1"))
}

func (a *App) apiBookNotes(w http.ResponseWriter, r *http.Request) {
	notes := a.store.BookNotes(r.PathValue("id"))
	if notes == nil {
		notes = []Note{}
	}
	writeJSON(w, 200, notes)
}

// apiAddNote: POST /api/books/{id}/notes {"text": "...", "author": "claude"|"utente"}
// (author defaults to "claude": the API is used by the agent).
func (a *App) apiAddNote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		Author string `json:"author"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Author != NoteByUser {
		req.Author = NoteByClaude
	}
	n, err := a.store.AddNote(r.PathValue("id"), req.Author, req.Text)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (a *App) apiDeleteNote(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteNote(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": r.PathValue("id")})
}
