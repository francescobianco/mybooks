# mybooks

Self-hosted personal library of PDF/EPUB books, designed to be browsed and
downloaded from a **Kobo** e-reader browser, and organised by a coding agent
through a JSON API.

- Single Go binary, no dependencies, data in `library.json` + files
- Minimal e-ink friendly UI (no JavaScript, paginated, big tap targets)
- One secret, `MYBOOKS_API_KEY`: password on the Kobo (session lasts 90 days), API key for the agent
- HTTPS via Caddy + Let's Encrypt, fully automatic
- Automatic covers from the first PDF page (poppler)

## Deploy

```sh
cp .env.example .env   # fill in SSH_HOST, MYBOOKS_API_KEY, MYBOOKS_DOMAIN
./provision.sh         # idempotent: run again anytime to update
```

Requirements: Go locally; a Debian/Ubuntu server reachable by SSH (root or
passwordless sudo), DNS of `MYBOOKS_DOMAIN` pointing to it, ports 80/443 open.

On the Kobo: *More → Beta features → Web browser*, open `https://<domain>`,
type the password, tap a book → *Download*.

## API

See [CLAUDE.md](CLAUDE.md).
