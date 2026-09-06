# Git Sync Feature Implementation Plan

## Overview
Add two-way Git synchronization to poly-txt, enabling users to:
- Clone Git repositories as new projects
- Pull updates from remote repositories
- Push local changes to remote repositories

## Technology Choice
**Use `go-git` library** (`github.com/go-git/go-git/v5`) - pure Go, no external binary dependency.

---

## Implementation Steps

### 1. Add Dependencies
```bash
go get github.com/go-git/go-git/v5
```

### 2. Database Schema (internal/db/db.go)
Add to the `migrate` function:
```sql
CREATE TABLE IF NOT EXISTS project_git_config (
    project_id TEXT PRIMARY KEY,
    remote_url TEXT NOT NULL,
    branch TEXT NOT NULL DEFAULT 'main',
    credentials_encrypted TEXT,
    last_sync TEXT,
    created TEXT DEFAULT (datetime('now')),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
) STRICT;
```

### 3. Create Git Package (internal/git/)

**internal/git/git.go** - Core operations:
- `Clone(targetDir, url, branch, creds)` - Clone repository
- `Pull(repoDir, creds)` - Pull changes from remote
- `Push(repoDir, creds, message, author)` - Commit and push
- `Status(repoDir)` - Get repo status (uncommitted changes, ahead/behind)

**internal/git/credentials.go** - Credential encryption:
- AES-256-GCM encryption using server's JWT secret
- `EncryptCredentials(creds, secret)` / `DecryptCredentials(encrypted, secret)`

### 4. New HTTP Handlers (internal/app/handlers.go)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/projects/clone` | POST | Clone repo as new project |
| `/api/projects/{id}/git/status` | GET | Get git status |
| `/api/projects/{id}/git/pull` | POST | Pull from remote |
| `/api/projects/{id}/git/push` | POST | Commit & push to remote |
| `/api/projects/{id}/git/config` | POST | Update git settings |

### 5. Routes (internal/app/routes.go)
Add inside authenticated group:
```go
r.Post("/projects/clone", s.handleCloneProject)
r.Get("/api/projects/{id}/git/status", s.handleGitStatus)
r.Post("/api/projects/{id}/git/pull", s.handleGitPull)
r.Post("/api/projects/{id}/git/push", s.handleGitPush)
r.Post("/api/projects/{id}/git/config", s.handleGitConfig)
```

### 6. UI Changes

**projects.html** - Add "Clone from Git" button with modal form:
- URL input (required)
- Project name (optional)
- Branch (default: main)
- Username/Token (optional, for private repos)

**editor.html** - Add Git section to Settings panel:
- Show remote URL and branch
- Pull/Push buttons
- Status indicator (uncommitted changes, sync state)
- Setup button for non-git projects

**editor.js** - Add functions:
- `loadGitStatus()` - Fetch and display git status
- `gitPull()` - Pull with status feedback
- `gitPush()` - Prompt for commit message, save files, push

### 7. Access Control
- **Clone**: Any authenticated user (creates new project)
- **Pull/Push**: Owner, Admin, Writer roles
- **Configure**: Owner, Admin only

---

## Files to Modify/Create

| File | Action |
|------|--------|
| `go.mod` | Add go-git dependency |
| `internal/db/db.go` | Add git_config table |
| `internal/git/git.go` | Create - git operations |
| `internal/git/credentials.go` | Create - encryption utils |
| `internal/app/handlers.go` | Add 5 new handlers |
| `internal/app/routes.go` | Add 5 new routes |
| `internal/app/templates/projects.html` | Add clone button/modal |
| `internal/app/templates/editor.html` | Add git panel to settings |
| `internal/app/static/editor.js` | Add git JS functions |

---

## Verification

1. **Clone test**: Clone a public GitHub repo, verify files appear
2. **Private repo test**: Clone with credentials, verify auth works
3. **Pull test**: Make change on GitHub, pull in app, verify update
4. **Push test**: Edit file in app, push, verify commit on GitHub
5. **Conflict test**: Edit same file both places, verify conflict detection
6. **Permissions test**: Verify readers cannot push, writers can
