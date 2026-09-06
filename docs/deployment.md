# Deployment Guide

This guide covers building the poly-txt Docker image, running it, and deploying
it to a server with TLS. There is also a section for running without Docker.

## What gets deployed

poly-txt is a single Go binary. Templates and static assets are compiled into
the binary, and the SQLite driver is pure Go, so the binary itself has no shared
library dependencies. At runtime it shells out to three external tools:

- `tectonic` to compile `.tex` files
- `typst` to compile `.typ` and `.md` files
- `git` (with `ssh`) for the Git integration

The provided `Dockerfile` bundles all three, so the container is self contained
apart from outbound network access (see [Compilers and network](#compilers-and-network)).

State lives entirely under one directory (`DATA_DIR`, `/data` in the image):

- `/data/latex.db`: SQLite database (users, projects, comments, keys)
- `/data/projects/<project-id>/`: project files and compiled PDFs
- `/data/cache/`: Tectonic support bundle cache

Persist `/data` and you have persisted everything.

## Prerequisites

- Docker 20.10 or newer (Buildx is included in modern Docker and is used for the
  multi architecture build args).
- A server reachable on the port you plan to expose.
- Optional but recommended: a domain name and a reverse proxy for TLS.

## Quick start with Docker Compose

The fastest path. From the repository root:

```bash
# Generate a signing secret once and keep it safe.
export JWT_SECRET=$(openssl rand -hex 32)

docker compose up -d --build
```

Then open `http://localhost:3000` and register the first user, who becomes the
admin (see [First run](#first-run)).

The Compose file stores data in a named volume (`poly-txt-data`) and restarts the
container unless you stop it. To store the secret with the project instead of an
exported variable, create a `.env` file next to `docker-compose.yml`:

```dotenv
JWT_SECRET=replace-with-a-long-random-value
```

Stop and remove the container (the data volume is kept):

```bash
docker compose down
```

## Building the image manually

```bash
docker build -t poly-txt:latest .
```

`make docker-build` does the same thing.

### Build arguments

| Arg | Default | Purpose |
| --- | --- | --- |
| `TYPST_VERSION` | `0.15.1` | Typst release to download. Must be 0.14.0 or newer, because Markdown rendering uses the `cmarker` package which requires it. |
| `TARGETARCH` | set by Docker | Selects the `x86_64` or `aarch64` binaries. Both `amd64` and `arm64` are supported. |

To pin a different Typst version:

```bash
docker build --build-arg TYPST_VERSION=0.15.1 -t poly-txt:latest .
```

### Building for another architecture

Docker builds for the host architecture by default. To build for a different one,
use Buildx:

```bash
docker buildx build --platform linux/amd64 -t poly-txt:latest --load .
```

## Running with `docker run`

```bash
docker run -d \
  --name poly-txt \
  -p 3000:3000 \
  -e JWT_SECRET="$(openssl rand -hex 32)" \
  -v poly-txt-data:/data \
  --restart unless-stopped \
  poly-txt:latest
```

`make docker-run` builds the image and runs it with a random secret and the same
named volume, which is handy for a quick local trial.

## Configuration

All configuration is through environment variables. The image sets sensible
defaults for everything except `JWT_SECRET`.

| Variable | Image default | Description |
| --- | --- | --- |
| `JWT_SECRET` | none, must be set | Secret used to sign session cookies. Use a long random value. Rotating it logs everyone out. |
| `PORT` | `3000` | HTTP listen port inside the container |
| `DATA_DIR` | `/data` | Base directory for the database and project files |
| `TECTONIC_BIN` | `tectonic` | Path to the tectonic binary |
| `TYPST_BIN` | `typst` | Path to the typst binary |
| `GIT_BIN` | `git` | Path to the git binary |

Keep `JWT_SECRET` stable across restarts and deploys. If it changes, existing
session cookies stop validating and users have to sign in again.

## Data persistence and backups

The container writes only to `/data`. Back it up by archiving that volume while
the app is stopped or quiet:

```bash
# Create a tarball of the named volume in the current directory.
docker run --rm \
  -v poly-txt-data:/data \
  -v "$PWD":/backup \
  alpine tar czf /backup/poly-txt-backup.tar.gz -C /data .
```

Restore into a fresh volume:

```bash
docker run --rm \
  -v poly-txt-data:/data \
  -v "$PWD":/backup \
  alpine sh -c "cd /data && tar xzf /backup/poly-txt-backup.tar.gz"
```

### Using a host directory instead of a named volume

You can bind mount a host path (`-v /srv/poly-txt:/data`). The container runs as
a non root user (`poly`, UID 1000 on Alpine), so the host directory must be
writable by that UID:

```bash
sudo mkdir -p /srv/poly-txt
sudo chown -R 1000:1000 /srv/poly-txt
```

## Compilers and network

Tectonic downloads its TeX support bundle from the internet the first time it
compiles a `.tex` document, then caches it. The image points the cache at
`/data/cache`, so as long as `/data` is persisted the download happens only once.
The container needs outbound HTTPS for that first compile and for cloning Git
repositories over HTTPS.

Typst downloads any `@preview` packages it needs on first use, including
`cmarker`, which is what renders Markdown. That download is cached under
`/data/cache` as well.

Two version requirements matter:

- Typst must be **0.14.0 or newer**. Markdown rendering goes through the
  `cmarker` package, which refuses to run on older versions.
- Tectonic can be any recent build. poly-txt calls the V1 command line
  (`tectonic file.tex`) rather than `tectonic -X compile`, because the V2
  interface only exists in builds compiled with the `serialization` feature,
  which distribution packages (including Alpine's) usually omit.

If your server has no outbound access, the first `.tex` compile will fail. In
that case, warm the cache on a machine that does have access and copy the
`/data/cache` directory over, or restrict usage to Typst and Markdown.

## Running behind a reverse proxy with TLS

Do not expose the container directly on the public internet over plain HTTP. Put
it behind a reverse proxy that terminates TLS. The session cookie is `HttpOnly`
and `SameSite=Strict`, and serving the app only over HTTPS keeps the cookie off
the wire in the clear.

### Caddy (automatic HTTPS)

`/etc/caddy/Caddyfile`:

```caddy
poly-txt.com {
    reverse_proxy localhost:3000
}
```

Caddy obtains and renews a certificate automatically. Run poly-txt bound to
localhost (for example `-p 127.0.0.1:3000:3000`) so it is reachable only through
the proxy.

### nginx

```nginx
server {
    listen 443 ssl;
    server_name poly-txt.com;

    ssl_certificate     /etc/letsencrypt/live/poly-txt.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/poly-txt.com/privkey.pem;

    # PDF uploads and downloads can be sizeable.
    client_max_body_size 50m;

    location / {
        proxy_pass         http://127.0.0.1:3000;
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
    }
}

server {
    listen 80;
    server_name poly-txt.com;
    return 301 https://$host$request_uri;
}
```

Obtain the certificate with certbot (`certbot --nginx -d poly-txt.com`) or your
preferred ACME client.

## First run

1. Open the site and go to `/register`.
2. The first registered user automatically becomes the admin.
3. After at least one user exists, public registration is disabled.
4. The admin creates further accounts from `/admin/users` and can reset any
   user's password there.

## Updating

```bash
git pull
docker compose up -d --build
```

The database schema migrates automatically on startup. Back up `/data` before a
major update as a precaution.

## Health check

The image ships a `HEALTHCHECK` that requests `/login`. Inspect it with:

```bash
docker inspect --format '{{.State.Health.Status}}' poly-txt
```

For an external monitor, poll `GET /login`, which returns `200` without
authentication.

## Security notes

- Set a strong, unique `JWT_SECRET` and treat it as a secret. Do not commit it.
- Terminate TLS at a reverse proxy and bind the container to localhost.
- The container already runs as a non root user.
- Consider resource limits in production, for example
  `docker run --memory=1g --cpus=2 ...`. Compilation is the heaviest operation.
- The session cookie is `HttpOnly` and `SameSite=Strict` but is not marked
  `Secure`. Serving only over HTTPS is what keeps it protected in transit.

## Running without Docker

You can run the binary directly if you prefer. The host needs `tectonic`,
`typst`, and `git` on the `PATH`.

```bash
# Build (produces bin/poly-txt).
make build

# Run.
JWT_SECRET="$(openssl rand -hex 32)" \
DATA_DIR=/var/lib/poly-txt \
PORT=3000 \
./bin/poly-txt
```

### systemd unit

`/etc/systemd/system/poly-txt.service`:

```ini
[Unit]
Description=poly-txt
After=network-online.target
Wants=network-online.target

[Service]
User=poly-txt
Group=poly-txt
Environment=PORT=3000
Environment=DATA_DIR=/var/lib/poly-txt
EnvironmentFile=/etc/poly-txt.env
ExecStart=/usr/local/bin/poly-txt
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/poly-txt

[Install]
WantedBy=multi-user.target
```

Put the secret in `/etc/poly-txt.env` (readable only by root):

```dotenv
JWT_SECRET=replace-with-a-long-random-value
```

Then:

```bash
sudo useradd --system --home /var/lib/poly-txt --create-home poly-txt
sudo cp bin/poly-txt /usr/local/bin/poly-txt
sudo systemctl daemon-reload
sudo systemctl enable --now poly-txt
```

## Troubleshooting

| Symptom | Likely cause and fix |
| --- | --- |
| Container exits immediately | `JWT_SECRET` unset with Compose, or `/data` not writable. Check `docker logs poly-txt`. |
| First `.tex` compile hangs or fails | No outbound network for the Tectonic bundle download. See [Compilers and network](#compilers-and-network). |
| Git clone over SSH fails | The remote host key or the deploy key is not set up. Add the account SSH public key (Settings page) to the Git host. |
| Permission denied writing to a bind mount | The host directory is not writable by UID 1000. `chown -R 1000:1000` the directory. |
| Everyone got logged out after a deploy | `JWT_SECRET` changed. Keep it stable across deploys. |
| `package requires typst 0.14.0 or newer` | The image has an older Typst. Rebuild with `--build-arg TYPST_VERSION=0.15.1` or newer. |
| `the "V2" Tectonic CLI requires ... the "serialization" Cargo feature` | A tectonic build without V2 support. poly-txt uses the V1 CLI, so make sure you are on a current build of poly-txt. |
