package main

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

//go:embed web/templates/*.html
var templateFS embed.FS

//go:embed web/static
var staticFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"size": func(n int64) string {
		switch {
		case n >= 1<<20:
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		case n >= 1<<10:
			return fmt.Sprintf("%d KB", n>>10)
		}
		return fmt.Sprintf("%d B", n)
	},
	"coverURL": func(b *Book) string {
		return fmt.Sprintf("/b/%s/cover?v=%d", b.ID, b.UpdatedAt.Unix())
	},
}).ParseFS(templateFS, "web/templates/*.html"))

func serveStatic(w http.ResponseWriter, r *http.Request) {
	sub, _ := fs.Sub(staticFS, "web")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.FileServer(http.FS(sub)).ServeHTTP(w, r)
}

func render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if a.authenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	render(w, "login.html", map[string]any{"Title": "Login"})
}

func (a *App) loginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !a.checkKey(r.PostFormValue("password")) {
		time.Sleep(2 * time.Second) // slow down guessing
		w.WriteHeader(http.StatusUnauthorized)
		render(w, "login.html", map[string]any{"Title": "Login", "Error": "Password errata"})
		return
	}
	a.setSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) homePage(w http.ResponseWriter, r *http.Request) {
	recent := a.store.List(Query{Sort: "added"})
	total := len(recent)
	if len(recent) > 6 {
		recent = recent[:6]
	}
	render(w, "home.html", map[string]any{
		"Title":         "Libreria",
		"Categories":    a.store.Children(""),
		"Uncategorized": a.store.UncategorizedCount(),
		"Recent":        recent,
		"Total":         total,
	})
}

// listPage handles /books (all, search, uncategorized) and /c/{slug}.
func (a *App) listPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := Query{Text: q.Get("q"), Tag: q.Get("tag"), Sort: q.Get("sort"), Category: q.Get("category"),
		Recursive: q.Get("recursive") == "1"}
	title := "Tutti i libri"
	var crumbs []Category
	var subcats []CategoryCount
	if slug := r.PathValue("slug"); slug != "" {
		query.Category = slug
	}
	if query.Category != "" && query.Category != "-" {
		c, err := a.store.Category(query.Category)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		title = c.Name
		crumbs = a.store.Path(c.Slug)
		crumbs = crumbs[:len(crumbs)-1]
		if query.Recursive {
			title = "Tutto in " + c.Name
		} else {
			subcats = a.store.Children(c.Slug)
		}
	} else if query.Category == "-" {
		title = "Senza categoria"
	}
	if query.Text != "" {
		title = "Ricerca: " + query.Text
	} else if query.Tag != "" {
		title = "Tag: " + query.Tag
	}

	books := a.store.List(query)
	size := a.cfg.PageSize
	pages := (len(books) + size - 1) / size
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	if page > pages && pages > 0 {
		page = pages
	}
	start := (page - 1) * size
	end := start + size
	if end > len(books) {
		end = len(books)
	}

	pageURL := func(p int) string {
		v := url.Values{}
		for k := range q {
			v.Set(k, q.Get(k))
		}
		v.Set("page", strconv.Itoa(p))
		return r.URL.Path + "?" + v.Encode()
	}
	sortQ := url.Values{}
	for k := range q {
		if k != "sort" && k != "page" {
			sortQ.Set(k, q.Get(k))
		}
	}
	sortURL := r.URL.Path + "?"
	if len(sortQ) > 0 {
		sortURL += sortQ.Encode() + "&"
	}
	data := map[string]any{
		"SortURL": sortURL + "sort=",
		"Crumbs":  crumbs,
		"Subcats": subcats,
		"Slug":    query.Category,
		"Title":   title,
		"Books":   books[start:end],
		"Count":   len(books),
		"Page":    page,
		"Pages":   pages,
		"Query":   query.Text,
		"Sort":    query.Sort,
	}
	if page > 1 {
		data["Prev"] = pageURL(page - 1)
	}
	if page < pages {
		data["Next"] = pageURL(page + 1)
	}
	render(w, "list.html", data)
}

func (a *App) bookPage(w http.ResponseWriter, r *http.Request) {
	b, err := a.store.Get(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Title": b.Title, "Book": b}
	if b.Category != "" {
		data["CategoryPath"] = a.store.Path(b.Category)
	}
	render(w, "book.html", data)
}
