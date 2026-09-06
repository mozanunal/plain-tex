# poly-txt

poly-txt is a self-hosted collaborative editor for LaTeX/Typst projects. It provides a browser-based workspace with file management, compilation, PDF preview, comments, and role-based project access.

## Features

- Email/password authentication with JWT session cookies.
- First registered user becomes admin automatically.
- Multi-project workspace with owner/admin/member roles.
- Per-project file tree with create, upload, rename, move, delete, and preview.
- Compilation support for `.tex`, `.typ`, and `.md` entry files.
- In-browser editor (Monaco) plus PDF viewer (pdf.js).
- Source-to-PDF and PDF-to-source jump support.
- Line-based comments on text files.
- Download compiled PDF or full project source zip.
- SQLite-backed metadata storage.

## Tech Stack

- Go 1.22
- `chi` router
- SQLite (`modernc.org/sqlite`)
- HTML templates + Tailwind CDN
- Monaco Editor + pdf.js (CDN)
- External compilers:
  - Tectonic (`.tex`)
  - Typst (`.typ`, `.md`)

## Requirements

- Go 1.22+
- `tectonic` installed and available on `PATH` (or set `TECTONIC_BIN`)
- `typst` 0.14.0 or newer on `PATH` (or set `TYPST_BIN`). Markdown rendering uses
  the `cmarker` Typst package, which requires 0.14.0+.
- Optional for CSS targets in `Makefile`: Node.js + `npx`

## Quick Start

```bash
go run ./cmd/server
```

Open `http://localhost:3000`.

Account bootstrap flow:

1. Register the first user at `/register`.
2. That first user is admin.
3. After at least one user exists, public registration is disabled.
4. Admins create additional users from `/admin/users`.

## Deployment

Run it with Docker:

```bash
JWT_SECRET=$(openssl rand -hex 32) docker compose up -d --build
```

See [docs/deployment.md](docs/deployment.md) for the full guide, covering image
builds, configuration, data persistence and backups, TLS via a reverse proxy,
updates, and running without Docker.

## Configuration

Environment variables:

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `3000` | HTTP listen port |
| `JWT_SECRET` | `change-me-in-production` | JWT signing secret (set this in production) |
| `DATA_DIR` | `data` | Base directory for SQLite DB and project files |
| `TECTONIC_BIN` | `tectonic` | Path to tectonic binary |
| `TYPST_BIN` | `typst` | Path to typst binary |

Runtime data layout:

- `data/latex.db`: SQLite database
- `data/projects/<project-id>/`: project files and compilation artifacts

## Compile Behavior

- Allowed compile entry extensions: `.tex`, `.typ`, `.md`.
- If no `entry` is provided, default selection order is:
  1. `main.tex`
  2. `main.typ`
  3. `main.md`
  4. `README.md`
  5. First lexicographic compilable file in project tree
- Markdown compilation is executed through Typst using a generated wrapper file.

## Access Model

- `admin`: full access to all projects and user management.
- `owner`: full access to owned project.
- `writer`: read + write + compile.
- `commenter`: read + comment.
- `reader`: read-only.

## Development

Make targets:

```bash
make help
make dev
make build
make css
make css-build
make clean
```

Run tests:

```bash
go test ./...
```

Current test status in this workspace: passing.

## HTTP Routes (high level)

Auth:

- `GET/POST /login`
- `GET/POST /register`
- `POST /logout`

Projects and admin:

- `GET /`
- `GET /admin/users`
- `POST /admin/users`
- `POST /projects`
- `DELETE /projects/{id}`
- `POST /projects/{id}/members`
- `POST /projects/{id}/members/{userID}/remove`

Editor and compile:

- `GET /editor/{id}`
- `POST /compile/{id}`
- `POST /save/{id}`
- `GET /pdf/{id}`
- `GET /api/projects/{id}/download/source`
- `GET /api/projects/{id}/download/pdf`

Files and comments:

- `GET /api/projects/{id}/files`
- `POST /api/projects/{id}/files`
- `POST /api/projects/{id}/files/move`
- `POST /api/projects/{id}/upload`
- `GET/PUT/DELETE /api/projects/{id}/files/*`
- `GET/POST /api/projects/{id}/comments`
- `DELETE /api/projects/{id}/comments/{commentID}`

## Project Structure

```text
cmd/server/main.go           # app entrypoint and env wiring
internal/app/                # HTTP server, handlers, templates, compile logic
internal/db/db.go            # SQLite schema and migrations
data/                        # local runtime data (ignored by git)
```
