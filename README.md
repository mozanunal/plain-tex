# poly-txt

**A self-hosted paper writing and collaboration platform for research labs and teams.**

poly-txt gives your group a shared browser workspace for writing papers, proposals,
and reports in LaTeX, Typst, or Markdown, without handing unpublished work to someone
else's cloud. You run it. Your data stays on your disk, in a shape you can read, back
up, and walk away with at any time.

Named for the formats it speaks: many text formats, one workspace.

---

## Why another editor

Most collaborative writing tools ask you to trade ownership for convenience. For a lab
working on unpublished results, a grant proposal, or anything under embargo, that
trade is often not acceptable, and the exit path usually means exporting a zip and
losing the history.

poly-txt takes the opposite position:

- **Your data is two things you already understand.** A directory of plain files, and
  one SQLite database. Nothing else.
- **No lock-in.** Projects are ordinary `.tex`, `.typ`, and `.md` files in ordinary
  folders. Open them in any editor. Push them to your own Git remote.
- **Backup is `tar`.** Migration is moving a folder to another machine.
- **One binary.** No external database, no message broker, no cluster.

## Your data, in one place

Everything poly-txt knows lives under a single directory (`DATA_DIR`):

```text
data/
├── latex.db                  # SQLite: users, projects, roles, comments, Git config
└── projects/
    ├── 3f2a.../              # one folder per project, plain files on disk
    │   ├── main.tex
    │   ├── sections/
    │   ├── figures/
    │   └── references.bib
    └── 8c41.../
```

Back the whole thing up:

```bash
tar czf poly-txt-backup.tar.gz -C /path/to/data .
```

Move it to a new server, point `DATA_DIR` at it, and you are running again. That is
the entire migration story.

## Features

**Writing and compiling**

- LaTeX via [Tectonic](https://tectonic-typesetting.github.io/), Typst, and Markdown
  (rendered through Typst).
- Monaco editor with syntax highlighting, and a pdf.js preview beside it.
- Split, editor-only, and PDF-only layouts.
- Pick any `.tex`, `.typ`, or `.md` file as the compile entry point straight from the
  file browser. The choice is stored per project, so it survives reloads.
- Click a spot in the PDF to jump to the approximate matching source line.
- Download the compiled PDF, or the whole project as a zip.

**Collaboration**

- Per-project membership with five roles: owner, admin, writer, commenter, reader.
- Line-anchored comments on any text file, for review passes without email threads.
- Admins manage accounts centrally and can reset any user's password.

**Git, as a first-class citizen**

- Clone an existing repository straight into a project.
- Commit, pull, and push from the editor sidebar.
- Each user gets their own SSH key, generated in-app, with the private half encrypted
  at rest. Add the public key to GitHub, GitLab, or your own host.
- Live ahead/behind counts, and a guided recovery path when a branch diverges: replay
  your commits with a rebase, or discard them to match the remote.
- Build artifacts are kept out of your commits automatically.

**Operations**

- Single static Go binary. Templates and assets are compiled in.
- Container image ships Tectonic, Typst, Git, SSH, and the fonts LaTeX documents
  usually expect, including Arial.
- `GET /healthz` for load balancers and uptime monitors.

## Quick start

```bash
git clone https://github.com/mozanunal/poly-txt.git
cd poly-txt

JWT_SECRET=$(openssl rand -hex 32) docker compose up -d --build
```

Open <http://localhost:3000> and register. **The first account you create becomes the
admin**, and public registration closes immediately afterwards. From there the admin
creates the rest of the team at `/admin/users`.

Keep that `JWT_SECRET` somewhere safe. It signs sessions **and** encrypts stored SSH
keys, so losing it means everyone signs in again and regenerates their keys.

### Running from source

Needs Go 1.22+, plus `tectonic`, `typst`, and `git` on your `PATH`. Typst must be
0.14.0 or newer for Markdown support.

```bash
make dev          # http://localhost:3000
make check        # format, vet, test
make build        # bin/poly-txt
```

## Configuration

Everything is environment variables.

| Variable | Default | Description |
| --- | --- | --- |
| `JWT_SECRET` | `change-me-in-production` | Signs session cookies and encrypts stored SSH private keys. Set it, and keep it stable. |
| `PORT` | `3000` | HTTP listen port |
| `DATA_DIR` | `data` | The one directory holding the database and all project files |
| `TECTONIC_BIN` | `tectonic` | Path to the tectonic binary |
| `TYPST_BIN` | `typst` | Path to the typst binary |
| `GIT_BIN` | `git` | Path to the git binary |

## Access model

| Role | Read | Comment | Write and compile | Manage members |
| --- | --- | --- | --- | --- |
| `owner` | yes | yes | yes | yes |
| `admin` | yes | yes | yes | yes |
| `writer` | yes | yes | yes | no |
| `commenter` | yes | yes | no | no |
| `reader` | yes | no | no | no |

Admin is instance-wide. The other roles are granted per project.

## Deployment

[docs/deployment.md](docs/deployment.md) is the full guide: building the image,
persistence and backups, TLS through Caddy or nginx, fonts, updates, health checks,
and a systemd unit for running without Docker.

## What this is not

Worth being clear before you adopt it:

- **Not a real-time co-editor.** There is no live cursor sharing or simultaneous
  typing in one file. Collaboration works the way most research teams already work:
  shared projects, roles, comments, and Git. Two people editing the same file at the
  same moment will overwrite each other.
- **Single node.** One process, one SQLite database, local disk. That is a deliberate
  choice in service of the backup story, not a stepping stone to a cluster.
- **PDF to source jumping is approximate.** It estimates the location rather than
  reading SyncTeX data.

## Tech stack

Go with the `chi` router, SQLite through the pure-Go `modernc.org/sqlite` driver (so
the binary is fully static), `html/template`, Tailwind, Monaco, and pdf.js.

## Contributing

Issues and pull requests are welcome. Please run `make check` before opening a PR.
