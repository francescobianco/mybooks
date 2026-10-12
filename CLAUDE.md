# mybooks — notes for coding agents

Personal PDF/EPUB library. Go, zero dependencies, plain HTTP on 127.0.0.1:8080
behind Caddy (automatic Let's Encrypt). Read by a Kobo browser (no JS, e-ink
friendly HTML in `web/`), managed through the JSON API below.

- `main.go` config/server · `store.go` library.json + files · `api.go` API · `auth.go` session/key · `web.go` + `web/` Kobo UI
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
| GET | `/api/books?q=&category=&tag=&sort=title\|author\|added` | `category=-` = uncategorized |
| POST | `/api/books` | multipart: `file` (PDF/EPUB, required), `cover` (JPEG/PNG), `metadata` (JSON). 409 + existing book if same sha256. PDF without cover → first page auto-rendered (`?autocover=0` to skip) |
| GET/PATCH/DELETE | `/api/books/{id}` | PATCH JSON: any of `title author description category tags series series_index language year publisher isbn` |
| GET/PUT | `/api/books/{id}/file` | PUT: multipart `file` or raw body |
| GET/PUT/DELETE | `/api/books/{id}/cover` | PUT: multipart `cover` or raw body |
| POST | `/api/books/{id}/cover/generate` | re-render first PDF page |
| GET | `/api/categories` | with book counts |
| POST | `/api/categories` | `{"name","slug"?,"description"?,"order"?}`; slug derived from name |
| PUT/DELETE | `/api/categories/{slug}` | delete leaves books uncategorized |

A book's `category` must be an existing slug — create the category first.

```sh
curl -fsS -H "$K" -X POST "$U/api/categories" -d '{"name":"Fantascienza"}'
curl -fsS -H "$K" -F file=@book.pdf \
  -F 'metadata={"title":"Dune","author":"Frank Herbert","category":"fantascienza","year":1965,"tags":["classici"]}' \
  "$U/api/books"
curl -fsS -H "$K" -X PATCH "$U/api/books/ID" -d '{"series":"Dune","series_index":1}'
curl -fsS -H "$K" -X PUT --data-binary @cover.jpg "$U/api/books/ID/cover"
```

Good covers: portrait JPEG ~600–800px wide, high contrast (e-ink is grayscale).
To pick a better page than the first: `pdftoppm -jpeg -f N -l N -scale-to 800 -singlefile book.pdf cover`.
For metadata, `pdfinfo book.pdf` and the first pages (`pdftotext -l 3`) usually contain title/author/ISBN.
