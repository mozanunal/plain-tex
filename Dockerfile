# syntax=docker/dockerfile:1

##########  Build the static Go binary  ##########
FROM golang:1.23-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src

# Download modules first so this layer caches across source changes.
COPY go.mod go.sum ./
RUN go mod download

# Build. Templates and static assets are embedded via go:embed, and the
# SQLite driver (modernc.org/sqlite) is pure Go, so the binary is fully static.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/poly-txt ./cmd/server

##########  Fetch the Typst compiler (static musl binary)  ##########
FROM alpine:3.20 AS typst
ARG TARGETARCH
# Markdown compiles through the cmarker Typst package, which requires 0.14.0+.
ARG TYPST_VERSION=0.15.1
RUN apk add --no-cache curl tar xz
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) arch="x86_64" ;; \
      arm64) arch="aarch64" ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    target="${arch}-unknown-linux-musl"; \
    curl -fsSL "https://github.com/typst/typst/releases/download/v${TYPST_VERSION}/typst-${target}.tar.xz" -o /tmp/typst.tar.xz; \
    mkdir -p /tmp/typst; \
    tar -xJf /tmp/typst.tar.xz -C /tmp/typst --strip-components=1; \
    install -m 0755 /tmp/typst/typst /usr/local/bin/typst; \
    /usr/local/bin/typst --version

##########  Runtime image  ##########
FROM alpine:3.20

# tectonic: LaTeX compiler (Alpine community). git + openssh-client: Git
# integration. ca-certificates: TLS for Git over HTTPS and the Tectonic bundle.
RUN apk add --no-cache ca-certificates git openssh-client tectonic \
    && adduser -D -h /home/poly poly

COPY --from=build /out/poly-txt /usr/local/bin/poly-txt
COPY --from=typst /usr/local/bin/typst /usr/local/bin/typst

ENV PORT=3000 \
    DATA_DIR=/data \
    HOME=/home/poly \
    XDG_CACHE_HOME=/data/cache
# JWT_SECRET has no default here on purpose. Set it at runtime.

RUN mkdir -p /data && chown -R poly:poly /data /home/poly

USER poly
WORKDIR /home/poly
VOLUME ["/data"]
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/login" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["poly-txt"]
