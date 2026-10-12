package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Book struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	Description string    `json:"description,omitempty"`
	Category    string    `json:"category,omitempty"` // category slug
	Tags        []string  `json:"tags,omitempty"`
	Series      string    `json:"series,omitempty"`
	SeriesIndex float64   `json:"series_index,omitempty"`
	Language    string    `json:"language,omitempty"`
	Year        int       `json:"year,omitempty"`
	Publisher   string    `json:"publisher,omitempty"`
	ISBN        string    `json:"isbn,omitempty"`
	Format      string    `json:"format"` // pdf | epub
	FileName    string    `json:"file_name"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	CoverExt    string    `json:"cover_ext,omitempty"` // "" when no cover
	AddedAt     time.Time `json:"added_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Category struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Order       int    `json:"order,omitempty"`
}

// BookPatch holds optional metadata fields; nil means "leave unchanged".
type BookPatch struct {
	Title       *string   `json:"title"`
	Author      *string   `json:"author"`
	Description *string   `json:"description"`
	Category    *string   `json:"category"`
	Tags        *[]string `json:"tags"`
	Series      *string   `json:"series"`
	SeriesIndex *float64  `json:"series_index"`
	Language    *string   `json:"language"`
	Year        *int      `json:"year"`
	Publisher   *string   `json:"publisher"`
	ISBN        *string   `json:"isbn"`
}

type library struct {
	Categories []Category `json:"categories"`
	Books      []*Book    `json:"books"`
}

type Store struct {
	dir string
	mu  sync.RWMutex
	lib library
}

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

func OpenStore(dir string) (*Store, error) {
	for _, sub := range []string{"books", "covers", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			return nil, err
		}
	}
	s := &Store{dir: dir}
	data, err := os.ReadFile(s.libPath())
	if err == nil {
		if err := json.Unmarshal(data, &s.lib); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) libPath() string         { return filepath.Join(s.dir, "library.json") }
func (s *Store) TmpDir() string          { return filepath.Join(s.dir, "tmp") }
func (s *Store) BookPath(b *Book) string { return filepath.Join(s.dir, "books", b.ID+"."+b.Format) }
func (s *Store) CoverPath(id, ext string) string {
	return filepath.Join(s.dir, "covers", id+"."+ext)
}

// save must be called with the write lock held.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.lib, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.libPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.libPath())
}

func newID() string {
	b := make([]byte, 5)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func copyBook(b *Book) *Book {
	c := *b
	c.Tags = append([]string(nil), b.Tags...)
	return &c
}

func (s *Store) find(id string) (int, *Book) {
	for i, b := range s.lib.Books {
		if b.ID == id {
			return i, b
		}
	}
	return -1, nil
}

func (s *Store) Get(id string) (*Book, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, b := s.find(id); b != nil {
		return copyBook(b), nil
	}
	return nil, ErrNotFound
}

func (s *Store) FindBySHA(sha string) *Book {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.lib.Books {
		if b.SHA256 == sha {
			return copyBook(b)
		}
	}
	return nil
}

type Query struct {
	Category string // slug; "-" means uncategorized
	Text     string
	Tag      string
	Sort     string // "title" (default), "added", "author"
}

func (s *Store) List(q Query) []*Book {
	s.mu.RLock()
	defer s.mu.RUnlock()
	text := fold(q.Text)
	var out []*Book
	for _, b := range s.lib.Books {
		if q.Category == "-" && b.Category != "" {
			continue
		}
		if q.Category != "" && q.Category != "-" && b.Category != q.Category {
			continue
		}
		if q.Tag != "" && !containsFold(b.Tags, q.Tag) {
			continue
		}
		if text != "" {
			hay := fold(strings.Join([]string{b.Title, b.Author, b.Series, b.Description, b.ISBN, strings.Join(b.Tags, " ")}, " "))
			if !strings.Contains(hay, text) {
				continue
			}
		}
		out = append(out, copyBook(b))
	}
	sortBooks(out, q.Sort)
	return out
}

func sortBooks(books []*Book, by string) {
	sort.SliceStable(books, func(i, j int) bool {
		a, b := books[i], books[j]
		switch by {
		case "added":
			return a.AddedAt.After(b.AddedAt)
		case "author":
			if fold(a.Author) != fold(b.Author) {
				return fold(a.Author) < fold(b.Author)
			}
		}
		if a.Series != "" && a.Series == b.Series {
			return a.SeriesIndex < b.SeriesIndex
		}
		return fold(a.Title) < fold(b.Title)
	})
}

func (s *Store) Add(b *Book) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.Category != "" && s.category(b.Category) == nil {
		return errors.New("unknown category: " + b.Category)
	}
	s.lib.Books = append(s.lib.Books, copyBook(b))
	return s.save()
}

func (s *Store) Update(id string, fn func(b *Book) error) (*Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, b := s.find(id)
	if b == nil {
		return nil, ErrNotFound
	}
	c := copyBook(b)
	if err := fn(c); err != nil {
		return nil, err
	}
	if c.Category != "" && s.category(c.Category) == nil {
		return nil, errors.New("unknown category: " + c.Category)
	}
	c.UpdatedAt = time.Now().UTC()
	*b = *c
	return copyBook(b), s.save()
}

func (s *Store) Delete(id string) (*Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, b := s.find(id)
	if b == nil {
		return nil, ErrNotFound
	}
	s.lib.Books = append(s.lib.Books[:i], s.lib.Books[i+1:]...)
	return b, s.save()
}

func (p BookPatch) Apply(b *Book) error {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&b.Title, p.Title)
	set(&b.Author, p.Author)
	set(&b.Description, p.Description)
	set(&b.Category, p.Category)
	set(&b.Series, p.Series)
	set(&b.Language, p.Language)
	set(&b.Publisher, p.Publisher)
	set(&b.ISBN, p.ISBN)
	if p.Tags != nil {
		b.Tags = nil
		for _, t := range *p.Tags {
			if t = strings.TrimSpace(t); t != "" && !containsFold(b.Tags, t) {
				b.Tags = append(b.Tags, t)
			}
		}
	}
	if p.SeriesIndex != nil {
		b.SeriesIndex = *p.SeriesIndex
	}
	if p.Year != nil {
		b.Year = *p.Year
	}
	if b.Title == "" {
		return errors.New("title cannot be empty")
	}
	return nil
}

// ---- categories ----

func (s *Store) category(slug string) *Category {
	for i := range s.lib.Categories {
		if s.lib.Categories[i].Slug == slug {
			return &s.lib.Categories[i]
		}
	}
	return nil
}

type CategoryCount struct {
	Category
	Count int `json:"count"`
}

func (s *Store) Categories() []CategoryCount {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int{}
	for _, b := range s.lib.Books {
		counts[b.Category]++
	}
	out := make([]CategoryCount, 0, len(s.lib.Categories))
	for _, c := range s.lib.Categories {
		out = append(out, CategoryCount{c, counts[c.Slug]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return fold(out[i].Name) < fold(out[j].Name)
	})
	return out
}

func (s *Store) Category(slug string) (Category, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.category(slug); c != nil {
		return *c, nil
	}
	return Category{}, ErrNotFound
}

func (s *Store) UncategorizedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, b := range s.lib.Books {
		if b.Category == "" {
			n++
		}
	}
	return n
}

func (s *Store) PutCategory(c Category) (Category, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Slug == "" {
		c.Slug = Slugify(c.Name)
	}
	if !slugRe.MatchString(c.Slug) {
		return c, errors.New("invalid slug (use a-z, 0-9 and -)")
	}
	if c.Name == "" {
		c.Name = c.Slug
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.category(c.Slug); existing != nil {
		*existing = c
	} else {
		s.lib.Categories = append(s.lib.Categories, c)
	}
	return c, s.save()
}

// DeleteCategory removes a category; its books become uncategorized.
func (s *Store) DeleteCategory(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.lib.Categories {
		if c.Slug == slug {
			s.lib.Categories = append(s.lib.Categories[:i], s.lib.Categories[i+1:]...)
			for _, b := range s.lib.Books {
				if b.Category == slug {
					b.Category = ""
				}
			}
			return s.save()
		}
	}
	return ErrNotFound
}

// ---- text helpers ----

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var accents = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ó", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
	"ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n", "ß", "ss", "œ", "oe", "æ", "ae",
)

// fold lowercases and strips common accents for search and sorting.
func fold(s string) string {
	return accents.Replace(strings.ToLower(s))
}

func Slugify(s string) string {
	var sb strings.Builder
	dash := false
	for _, r := range fold(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
			dash = false
		} else if !dash && sb.Len() > 0 {
			sb.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(sb.String(), "-")
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
