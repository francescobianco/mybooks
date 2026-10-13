package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type App struct {
	cfg   Config
	store *Store
}

func NewApp(cfg Config, store *Store) *App {
	return &App{cfg: cfg, store: store}
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// web (Kobo)
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginSubmit)
	mux.HandleFunc("POST /logout", a.logout)
	mux.HandleFunc("GET /logout", a.logout)
	mux.HandleFunc("GET /static/", serveStatic)
	mux.HandleFunc("GET /{$}", a.requireWeb(a.homePage))
	mux.HandleFunc("GET /books", a.requireWeb(a.listPage))
	mux.HandleFunc("GET /c/{slug}", a.requireWeb(a.listPage))
	mux.HandleFunc("GET /b/{id}", a.requireWeb(a.bookPage))
	mux.HandleFunc("GET /b/{id}/download", a.requireWeb(a.download))
	mux.HandleFunc("GET /b/{id}/cover", a.requireWeb(a.cover))

	// API
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": version})
	})
	mux.HandleFunc("GET /api/books", a.requireAPI(a.apiListBooks))
	mux.HandleFunc("POST /api/books", a.requireKey(a.apiCreateBook))
	mux.HandleFunc("GET /api/books/{id}", a.requireAPI(a.apiGetBook))
	mux.HandleFunc("PATCH /api/books/{id}", a.requireKey(a.apiPatchBook))
	mux.HandleFunc("DELETE /api/books/{id}", a.requireKey(a.apiDeleteBook))
	mux.HandleFunc("GET /api/books/{id}/file", a.requireAPI(a.download))
	mux.HandleFunc("PUT /api/books/{id}/file", a.requireKey(a.apiPutFile))
	mux.HandleFunc("GET /api/books/{id}/cover", a.requireAPI(a.cover))
	mux.HandleFunc("PUT /api/books/{id}/cover", a.requireKey(a.apiPutCover))
	mux.HandleFunc("POST /api/books/{id}/cover/generate", a.requireKey(a.apiGenerateCover))
	mux.HandleFunc("DELETE /api/books/{id}/cover", a.requireKey(a.apiDeleteCover))
	mux.HandleFunc("GET /api/categories", a.requireAPI(a.apiListCategories))
	mux.HandleFunc("POST /api/categories", a.requireKey(a.apiPutCategory))
	mux.HandleFunc("PUT /api/categories/{slug}", a.requireKey(a.apiPutCategory))
	mux.HandleFunc("PATCH /api/categories/{slug}", a.requireKey(a.apiPatchCategory))
	mux.HandleFunc("DELETE /api/categories/{slug}", a.requireKey(a.apiDeleteCategory))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	return mux
}

// requireKey protects mutating endpoints: only the API key is accepted, never
// the browser session cookie.
func (a *App) requireKey(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.validAPIKey(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadMB<<20)
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// ---- books ----

func (a *App) apiListBooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	books := a.store.List(Query{Category: q.Get("category"), Recursive: q.Get("recursive") == "1",
		Text: q.Get("q"), Tag: q.Get("tag"), Sort: q.Get("sort")})
	if books == nil {
		books = []*Book{}
	}
	writeJSON(w, 200, books)
}

func (a *App) apiGetBook(w http.ResponseWriter, r *http.Request) {
	b, err := a.store.Get(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, b)
}

// upload is a file received from the client and parked in the tmp dir.
type upload struct {
	path, name, sha string
	size            int64
	head            []byte
}

func (u *upload) discard() {
	if u != nil {
		os.Remove(u.path)
	}
}

func (a *App) saveUpload(src io.Reader, name string) (*upload, error) {
	f, err := os.CreateTemp(a.store.TmpDir(), "up-*")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	var head bytes.Buffer
	n, err := io.Copy(io.MultiWriter(f, h, &limitedBuffer{&head, 512}), src)
	if err != nil {
		os.Remove(f.Name())
		return nil, err
	}
	return &upload{path: f.Name(), name: filepath.Base(name), sha: hex.EncodeToString(h.Sum(nil)), size: n, head: head.Bytes()}, nil
}

type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.buf.Write(p[:room])
	}
	return len(p), nil
}

// receiveFile reads a single upload, either as the multipart field `field`
// or as the raw request body.
func (a *App) receiveFile(r *http.Request, field string) (*upload, error) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "multipart/form-data" {
		name := r.URL.Query().Get("filename")
		return a.saveUpload(r.Body, name)
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, fmt.Errorf("missing multipart field %q", field)
		}
		if err != nil {
			return nil, err
		}
		if part.FormName() == field {
			return a.saveUpload(part, part.FileName())
		}
	}
}

func bookFormat(u *upload) (string, error) {
	switch {
	case bytes.HasPrefix(u.head, []byte("%PDF")):
		return "pdf", nil
	case bytes.HasPrefix(u.head, []byte("PK")) && bytes.Contains(u.head, []byte("application/epub+zip")):
		return "epub", nil
	}
	return "", errors.New("unsupported file: only PDF and EPUB are accepted")
}

func coverExt(u *upload) (string, error) {
	switch http.DetectContentType(u.head) {
	case "image/jpeg":
		return "jpg", nil
	case "image/png":
		return "png", nil
	}
	return "", errors.New("unsupported cover: use JPEG or PNG")
}

// apiCreateBook accepts multipart/form-data with:
//
//	file     (required) the PDF/EPUB
//	cover    (optional) JPEG/PNG cover image
//	metadata (optional) JSON object with BookPatch fields
func (a *App) apiCreateBook(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, 400, "expected multipart/form-data")
		return
	}
	var file, cover *upload
	defer func() { file.discard(); cover.discard() }()
	var patch BookPatch
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		switch part.FormName() {
		case "file":
			file.discard()
			file, err = a.saveUpload(part, part.FileName())
		case "cover":
			cover.discard()
			cover, err = a.saveUpload(part, part.FileName())
		case "metadata":
			err = json.NewDecoder(io.LimitReader(part, 1<<20)).Decode(&patch)
		}
		if err != nil {
			writeError(w, 400, part.FormName()+": "+err.Error())
			return
		}
	}
	if file == nil {
		writeError(w, 400, "missing multipart field \"file\"")
		return
	}
	format, err := bookFormat(file)
	if err != nil {
		writeError(w, 415, err.Error())
		return
	}
	if dup := a.store.FindBySHA(file.sha); dup != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "book already exists", "book": dup})
		return
	}
	var cExt string
	if cover != nil {
		if cExt, err = coverExt(cover); err != nil {
			writeError(w, 415, err.Error())
			return
		}
	}

	now := time.Now().UTC()
	b := &Book{
		ID:        newID(),
		Title:     strings.TrimSuffix(file.name, filepath.Ext(file.name)),
		Format:    format,
		FileName:  file.name,
		Size:      file.size,
		SHA256:    file.sha,
		AddedAt:   now,
		UpdatedAt: now,
	}
	if b.Title == "" || b.Title == "." {
		b.Title = "Untitled"
	}
	if b.Format == "pdf" {
		b.Pages = pdfPages(r.Context(), file.path)
	}
	if err := patch.Apply(b); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := os.Rename(file.path, a.store.BookPath(b)); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if cover != nil {
		if err := os.Rename(cover.path, a.store.CoverPath(b.ID, cExt)); err == nil {
			b.CoverExt = cExt
		}
	}
	if err := a.store.Add(b); err != nil {
		os.Remove(a.store.BookPath(b))
		if b.CoverExt != "" {
			os.Remove(a.store.CoverPath(b.ID, b.CoverExt))
		}
		writeStoreError(w, err)
		return
	}
	if b.CoverExt == "" && b.Format == "pdf" && r.URL.Query().Get("autocover") != "0" {
		if nb, err := a.generateCover(r.Context(), b.ID); err == nil {
			b = nb
		} else {
			log.Printf("autocover %s: %v", b.ID, err)
		}
	}
	writeJSON(w, http.StatusCreated, b)
}

func (a *App) apiPatchBook(w http.ResponseWriter, r *http.Request) {
	var patch BookPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	b, err := a.store.Update(r.PathValue("id"), patch.Apply)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, b)
}

func (a *App) apiDeleteBook(w http.ResponseWriter, r *http.Request) {
	b, err := a.store.Delete(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	os.Remove(a.store.BookPath(b))
	if b.CoverExt != "" {
		os.Remove(a.store.CoverPath(b.ID, b.CoverExt))
	}
	writeJSON(w, 200, map[string]any{"deleted": b.ID})
}

func (a *App) apiPutFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	old, err := a.store.Get(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	u, err := a.receiveFile(r, "file")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	defer u.discard()
	format, err := bookFormat(u)
	if err != nil {
		writeError(w, 415, err.Error())
		return
	}
	if dup := a.store.FindBySHA(u.sha); dup != nil && dup.ID != id {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "file already belongs to another book", "book": dup})
		return
	}
	nb := copyBook(old)
	nb.Format = format
	if err := os.Rename(u.path, a.store.BookPath(nb)); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	pages := 0
	if format == "pdf" {
		pages = pdfPages(r.Context(), a.store.BookPath(nb))
	}
	b, err := a.store.Update(id, func(b *Book) error {
		b.Format, b.Size, b.SHA256, b.Pages = format, u.size, u.sha, pages
		if u.name != "" && u.name != "." {
			b.FileName = u.name
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if old.Format != format {
		os.Remove(a.store.BookPath(old))
	}
	writeJSON(w, 200, b)
}

func (a *App) setCover(id, srcPath, ext string) (*Book, error) {
	old, err := a.store.Get(id)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(srcPath, a.store.CoverPath(id, ext)); err != nil {
		return nil, err
	}
	if old.CoverExt != "" && old.CoverExt != ext {
		os.Remove(a.store.CoverPath(id, old.CoverExt))
	}
	return a.store.Update(id, func(b *Book) error { b.CoverExt = ext; return nil })
}

func (a *App) apiPutCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := a.store.Get(id); err != nil {
		writeStoreError(w, err)
		return
	}
	u, err := a.receiveFile(r, "cover")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	defer u.discard()
	ext, err := coverExt(u)
	if err != nil {
		writeError(w, 415, err.Error())
		return
	}
	b, err := a.setCover(id, u.path, ext)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, b)
}

func (a *App) apiDeleteCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	old, err := a.store.Get(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if old.CoverExt != "" {
		os.Remove(a.store.CoverPath(id, old.CoverExt))
	}
	b, err := a.store.Update(id, func(b *Book) error { b.CoverExt = ""; return nil })
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, b)
}

// generateCover renders the first page of a PDF with pdftoppm (poppler-utils).
func (a *App) generateCover(ctx context.Context, id string) (*Book, error) {
	b, err := a.store.Get(id)
	if err != nil {
		return nil, err
	}
	if b.Format != "pdf" {
		return nil, errors.New("cover generation is only available for PDF")
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return nil, errors.New("pdftoppm not installed on server")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out := filepath.Join(a.store.TmpDir(), "cover-"+b.ID)
	cmd := exec.CommandContext(ctx, "pdftoppm", "-jpeg", "-jpegopt", "quality=85", "-f", "1", "-l", "1",
		"-scale-to", "800", "-singlefile", a.store.BookPath(b), out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		os.Remove(out + ".jpg")
		return nil, fmt.Errorf("pdftoppm: %v: %s", err, strings.TrimSpace(string(msg)))
	}
	return a.setCover(id, out+".jpg", "jpg")
}

func (a *App) apiGenerateCover(w http.ResponseWriter, r *http.Request) {
	b, err := a.generateCover(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, b)
}

// pdfPages returns the page count reported by pdfinfo, or 0 if unknown.
func pdfPages(ctx context.Context, path string) int {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pdfinfo", path).Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "Pages:"); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// BackfillPages fills in the page count of PDFs uploaded before it was tracked.
func (a *App) BackfillPages() {
	for _, b := range a.store.List(Query{}) {
		if b.Format != "pdf" || b.Pages > 0 {
			continue
		}
		if n := pdfPages(context.Background(), a.store.BookPath(b)); n > 0 {
			a.store.Update(b.ID, func(b *Book) error { b.Pages = n; return nil })
		}
	}
}

// ---- categories ----

func (a *App) apiListCategories(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"categories":    a.store.Categories(),
		"uncategorized": a.store.UncategorizedCount(),
	})
}

func (a *App) apiPutCategory(w http.ResponseWriter, r *http.Request) {
	var c Category
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if slug := r.PathValue("slug"); slug != "" {
		c.Slug = slug
	}
	c, err := a.store.PutCategory(c)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (a *App) apiPatchCategory(w http.ResponseWriter, r *http.Request) {
	var p CategoryPatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	c, err := a.store.PatchCategory(r.PathValue("slug"), p)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (a *App) apiDeleteCategory(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteCategory(r.PathValue("slug")); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": r.PathValue("slug")})
}

// ---- files (shared by web and API) ----

func (a *App) download(w http.ResponseWriter, r *http.Request) {
	b, err := a.store.Get(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := b.Title
	if b.Author != "" {
		name = b.Author + " - " + b.Title
	}
	name = safeFilename(name) + "." + b.Format
	ctype := "application/pdf"
	if b.Format == "epub" {
		ctype = "application/epub+zip"
	}
	w.Header().Set("Content-Type", ctype)
	// Plain ASCII filename first for old e-reader browsers, RFC 5987 form after.
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		asciiFilename(name), url.PathEscape(name)))
	http.ServeFile(w, r, a.store.BookPath(b))
}

func (a *App) cover(w http.ResponseWriter, r *http.Request) {
	b, err := a.store.Get(r.PathValue("id"))
	if err != nil || b.CoverExt == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, a.store.CoverPath(b.ID, b.CoverExt))
}

func safeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
	if len(s) > 150 {
		s = s[:150]
	}
	return strings.TrimSpace(s)
}

func asciiFilename(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r > 126 {
			if f := accents.Replace(string(unicode.ToLower(r))); len(f) == 1 || f == "ss" || f == "oe" || f == "ae" {
				if unicode.IsUpper(r) {
					f = strings.ToUpper(f)
				}
				sb.WriteString(f)
				continue
			}
		}
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			r = '_'
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
