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
# fontconfig + fonts: fontspec/XeTeX resolves fonts by system name, and Tectonic
# downloads TeX packages but never fonts, so without these any document using
# \setmainfont fails with "The font ... cannot be found".
RUN apk add --no-cache ca-certificates git openssh-client tectonic fontconfig \
    && (apk add --no-cache font-liberation || apk add --no-cache ttf-liberation) \
    && (apk add --no-cache font-dejavu || apk add --no-cache ttf-dejavu) \
    && adduser -D -h /home/poly poly

# Microsoft core fonts, which is what \setmainfont{Arial} actually needs.
# XeTeX resolves font names by enumerating the installed families rather than by
# asking fontconfig to match, so a fontconfig alias is NOT sufficient: a font
# whose real family name is "Arial" has to be present. Build with
# --build-arg INSTALL_MS_FONTS=false to skip this (documents must then name a
# font that exists, such as "Liberation Sans").
ARG INSTALL_MS_FONTS=true
RUN if [ "$INSTALL_MS_FONTS" = "true" ]; then \
        apk add --no-cache msttcorefonts-installer && update-ms-fonts; \
    fi

# Fallback aliases for consumers that do honour fontconfig matching (Typst and
# anything else that asks fontconfig rather than enumerating). Liberation is
# metric-compatible with the Microsoft fonts, so widths, line breaks, and total
# page count are preserved.
RUN printf '%s\n' \
    '<?xml version="1.0"?>' \
    '<!DOCTYPE fontconfig SYSTEM "fonts.dtd">' \
    '<fontconfig>' \
    '  <alias binding="same"><family>Arial</family>' \
    '    <accept><family>Liberation Sans</family></accept></alias>' \
    '  <alias binding="same"><family>Helvetica</family>' \
    '    <accept><family>Liberation Sans</family></accept></alias>' \
    '  <alias binding="same"><family>Times New Roman</family>' \
    '    <accept><family>Liberation Serif</family></accept></alias>' \
    '  <alias binding="same"><family>Courier New</family>' \
    '    <accept><family>Liberation Mono</family></accept></alias>' \
    '</fontconfig>' > /etc/fonts/local.conf \
    && fc-cache -f

# Fail the build here rather than letting the first .tex compile discover a
# missing font at runtime. XeTeX needs a real family named "Arial".
RUN if [ "$INSTALL_MS_FONTS" = "true" ]; then \
        fc-list : family | tr ',' '\n' | grep -qx 'Arial' \
        || { echo "ERROR: Arial is not installed, so \\setmainfont{Arial} would fail at runtime." >&2; \
             echo "Rebuild with --build-arg INSTALL_MS_FONTS=false to accept Liberation substitutes." >&2; \
             exit 1; }; \
    fi

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
  CMD wget -qO- "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["poly-txt"]
