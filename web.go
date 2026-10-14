package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
	"upper": strings.ToUpper,
	"date": func(t time.Time) string {
		return t.In(time.Local).Format("02/01/2006 15:04")
	},
	"last": func(n []Note) Note { return n[len(n)-1] },
	"paragraphs": func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, "\n\n") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
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

const langCookie = "mybooks_lang"

var langRe = regexp.MustCompile(`^[a-z]{2,3}$`)

// lang returns the language filter chosen on this device ("" = all).
func (a *App) lang(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && langRe.MatchString(c.Value) {
		return c.Value
	}
	return ""
}

// setLang stores the language filter in a cookie and goes back to `next`.
func (a *App) setLang(w http.ResponseWriter, r *http.Request) {
	l := r.URL.Query().Get("l")
	if langRe.MatchString(l) {
		http.SetCookie(w, &http.Cookie{Name: langCookie, Value: l, Path: "/", MaxAge: 5 * 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
	} else {
		http.SetCookie(w, &http.Cookie{Name: langCookie, Value: "", Path: "/", MaxAge: -1})
	}
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	redirect(w, r, next)
}

// page renders a template adding what the layout needs (language bar).
func (a *App) page(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	data["Lang"] = a.lang(r)
	data["Langs"] = a.store.Languages()
	// current URL without the page number: switching language restarts from page 1
	q := r.URL.Query()
	q.Del("page")
	next := r.URL.Path
	if len(q) > 0 {
		next += "?" + q.Encode()
	}
	data["LangNext"] = next
	data["Base"] = base(r)
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(withBase(buf.Bytes(), base(r)))
}

// nonEmpty hides categories without books (in the selected language, if any).
func nonEmpty(cats []CategoryCount, lang string) []CategoryCount {
	var out []CategoryCount
	for _, c := range cats {
		if c.Total > 0 {
			out = append(out, c)
		}
	}
	return out
}

// serveRootStatic serves an embedded static file at a root path browsers
// request on their own (favicon, touch icon).
func serveRootStatic(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("web/static/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=604800")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	}
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
	render(w, "login.html", map[string]any{"Title": "Login", "Revoked": r.URL.Query().Has("revocato")})
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
	if r.PostFormValue("remember") != "" {
		// browsers that forget cookies (Kobo) get a permanent per-device link
		_, token, err := a.store.CreateDevice(r.PostFormValue("device"))
		if err == nil {
			http.Redirect(w, r, "/k/"+token+"/?benvenuto=1", http.StatusSeeOther)
			return
		}
		log.Printf("create device: %v", err)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) homePage(w http.ResponseWriter, r *http.Request) {
	lang := a.lang(r)
	recent := a.store.List(Query{Sort: "added", Language: lang})
	total := len(recent)
	if len(recent) > 6 {
		recent = recent[:6]
	}
	welcome := ""
	if r.URL.Query().Has("benvenuto") && base(r) != "" {
		welcome = deviceURL(r, strings.TrimPrefix(base(r), "/k/"))
	}
	a.page(w, r, "home.html", map[string]any{
		"Title":         "Libreria",
		"Welcome":       welcome,
		"Categories":    nonEmpty(a.store.Children("", lang), lang),
		"Uncategorized": a.store.UncategorizedCount(lang),
		"Recent":        recent,
		"Total":         total,
	})
}

// listPage handles /books (all, search, uncategorized) and /c/{slug}.
func (a *App) listPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := Query{Text: q.Get("q"), Tag: q.Get("tag"), Sort: q.Get("sort"), Category: q.Get("category"),
		Recursive: q.Get("recursive") == "1", Language: a.lang(r)}
	title := "Tutti i libri"
	var crumbs []Category
	back := "" // link to the level above, shown next to the title
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
		back = "/"
		if query.Recursive {
			back = "/c/" + c.Slug
		} else if len(crumbs) > 0 {
			back = "/c/" + crumbs[len(crumbs)-1].Slug
		}
		if query.Recursive {
			title = "Tutto in " + c.Name
		} else {
			subcats = nonEmpty(a.store.Children(c.Slug, query.Language), query.Language)
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
	// The heading counts sub-categories too; the list shows direct books only.
	total := len(books)
	if len(subcats) > 0 {
		rq := query
		rq.Recursive = true
		total = len(a.store.List(rq))
	}
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
		"Back":    back,
		"Subcats": subcats,
		"Slug":    query.Category,
		"Title":   title,
		"Books":   books[start:end],
		"Count":   len(books),
		"Total":   total,
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
	a.page(w, r, "list.html", data)
}

func (a *App) bookPage(w http.ResponseWriter, r *http.Request) {
	b, err := a.store.Get(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Title": b.Title, "Book": b, "ShowPreview": r.URL.Query().Has("anteprima"),
		"Notes": a.store.BookNotes(b.ID)}
	if b.Category != "" {
		data["CategoryPath"] = a.store.Path(b.Category)
	}
	a.page(w, r, "book.html", data)
}
