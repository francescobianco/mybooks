# mybooks — notes for coding agents

Personal PDF/EPUB library. Go, zero dependencies, plain HTTP on 127.0.0.1:8080
behind Caddy (automatic Let's Encrypt). Read by a Kobo browser (no JS, e-ink
friendly HTML in `web/`), managed through the JSON API below.

- `main.go` config/server · `store.go` library.json + files · `api.go` API · `auth.go` session/key · `web.go` + `web/` Kobo UI
- Device links: `/k/<token>/...` mirrors the whole web UI for a device (token stored hashed, read-only, revocable, redacted in logs); cookie sessions still work as before.
- Deploy: `./provision.sh` (idempotent, reads `.env`). Never print `MYBOOKS_API_KEY`.
- Local run: `MYBOOKS_API_KEY=devdevdevdev go run .` → http://127.0.0.1:8080

## Managing the library through the API

Load credentials from `.env` without echoing them:

```sh
set -a; . ./.env; set +a
K="X-API-Key: $MYBOOKS_API_KEY"; U="$MYBOOKS_URL"
```

| Method | Path | Notes |
|---|---|---|
| GET | `/api/books?q=&category=&recursive=1&lang=&tag=&sort=title\|author\|added` | `category=-` = uncategorized; `recursive=1` includes sub-categories; `lang=it` filters by language |
| POST | `/api/books` | multipart: `file` (PDF/EPUB, required), `cover` (JPEG/PNG), `metadata` (JSON). 409 + existing book if same sha256. PDF without cover → first page auto-rendered (`?autocover=0` to skip) |
| GET/PATCH/DELETE | `/api/books/{id}` | PATCH JSON: any of `title author description category tags series series_index language year publisher isbn pages` |
| GET/PUT | `/api/books/{id}/file` | PUT: multipart `file` or raw body |
| GET/PUT/DELETE | `/api/books/{id}/cover` | PUT: multipart `cover` or raw body |
| POST | `/api/books/{id}/cover/generate` | re-render first PDF page |
| GET | `/api/categories?lang=` | whole tree, depth-first, each with `parent`, `path` ("Scacchi / Finali"), `count` (direct) and `total` (with sub-categories); also `languages` present in the library |
| POST | `/api/categories` | `{"name","slug"?,"parent"?,"description"?,"order"?}`; slug derived from name, globally unique |
| PUT | `/api/categories/{slug}` | full replace (omitting `parent` makes it a root) |
| PATCH | `/api/categories/{slug}` | partial update, e.g. `{"parent":"informatica"}` to move it; cycles are rejected |
| DELETE | `/api/categories/{slug}` | its books and sub-categories move up to its parent |
| GET/POST | `/api/devices` | per-device permanent links for browsers that drop cookies (Kobo). POST `{"name"}` returns `token` and `url` once |
| DELETE | `/api/devices/{id}` | revoke a device link |

Categories are a tree: top-level ones are broad subjects (Scacchi, Informatica…), with topics below.
A book's `category` must be an existing slug — create the category first. Prefer putting books in the
most specific (leaf) category. Always set `language` as a lowercase ISO 639-1 code (`it`, `en`, `es`…):
the Kobo UI has a language filter built from it. Page count is filled in automatically: exact for PDFs (pdfinfo), estimated for EPUBs
(`pages_estimated: true`, shown as "~N pag."). If you know the real printed page count (e.g. from the PDF
edition of the same book) PATCH `pages`, which clears the estimate. PDF and EPUB covers are extracted automatically.
Prefer EPUB over PDF when both exist: it reflows on the Kobo.

```sh
curl -fsS -H "$K" -X POST "$U/api/categories" -d '{"name":"Narrativa"}'
curl -fsS -H "$K" -X POST "$U/api/categories" -d '{"name":"Fantascienza","parent":"narrativa"}'
curl -fsS -H "$K" -F file=@book.pdf \
  --form-string 'metadata={"title":"Dune","author":"Frank Herbert","category":"fantascienza","year":1965,"tags":["classici"]}' \
  "$U/api/books"
curl -fsS -H "$K" -X PATCH "$U/api/books/ID" -d '{"series":"Dune","series_index":1}'
curl -fsS -H "$K" -X PUT --data-binary @cover.jpg "$U/api/books/ID/cover"
```

Pass `metadata` with `--form-string`, not `-F`: curl treats `;` inside a `-F` value as an option
separator and silently truncates the JSON ("metadata: unexpected EOF").

Good covers: portrait JPEG ~600–800px wide, high contrast (e-ink is grayscale).
To pick a better page than the first: `pdftoppm -jpeg -f N -l N -scale-to 800 -singlefile book.pdf cover`.
For metadata, `pdfinfo book.pdf` and the first pages (`pdftotext -l 3`) usually contain title/author/ISBN.
