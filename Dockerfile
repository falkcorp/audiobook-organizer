# file: Dockerfile
# version: 2.7.0
# guid: audiobook-organizer-dockerfile-production
# last-edited: 2026-09-01

# Multi-stage production Dockerfile for audiobook-organizer
# Builds React frontend, embeds it into a statically-linked Go binary with
# CGO enabled (for SQLite FTS5 support), produces a minimal container.

# Stage 1: Build frontend
# SHA pinned 2026-06-23 (node:26-alpine manifest-list). Refresh with:
#   docker buildx imagetools inspect node:26-alpine --format '{{.Manifest.Digest}}'
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:2d984a15c9b54fd0aeb608b8e0d0d83529eb34d2966db27a1fb4f1edc3d298a3 AS frontend-builder

WORKDIR /build/web

COPY web/package*.json ./
RUN npm ci --prefer-offline --no-audit

COPY web/ ./
RUN npm run build

# Stage 2: Build Go application with embedded frontend
# Uses native platform (no cross-compile) so CGO works without cross-toolchain.
# SHA pinned 2026-09-01 (golang:1.27.1-alpine manifest-list). Keep in step with
# the Makefile's GOTOOLCHAIN pin.
FROM golang:1.27.1-alpine@sha256:3f6d04dc61331ee3c2fbbaad62d54412a84680f6a041d269a20a5270a078515b AS go-builder

WORKDIR /build

RUN apk add --no-cache git gcc g++ musl-dev sqlite-dev ca-certificates tzdata \
    cmake make curl zlib-dev zlib-static

# Build TagLib static libraries for native CGO bindings
RUN set -ex \
    && mkdir -p /tmp/taglib-build/install/lib /tmp/taglib-build/install/include \
    && cd /tmp/taglib-build \
    # utfcpp (header-only taglib dependency)
    && curl -sL -o utfcpp.tar.gz https://github.com/nemtrif/utfcpp/archive/refs/tags/v4.0.6.tar.gz && echo "6920a6a5d6a04b9a89b2a89af7132f8acefd46e0c2a7b190350539e9213816c0  utfcpp.tar.gz" | sha256sum -c - && tar xzf utfcpp.tar.gz \
    # taglib (uses system zlib from apk)
    && curl -sL -o taglib.tar.gz https://github.com/taglib/taglib/releases/download/v2.0.2/taglib-2.0.2.tar.gz && echo "0de288d7fe34ba133199fd8512f19cc1100196826eafcb67a33b224ec3a59737  taglib.tar.gz" | sha256sum -c - && tar xzf taglib.tar.gz \
    && mkdir -p build && cd build \
    && cmake ../taglib-2.0.2 \
         -DCMAKE_INSTALL_PREFIX=/tmp/taglib-build/install \
         -DBUILD_SHARED_LIBS=OFF -DBUILD_EXAMPLES=OFF -DBUILD_TESTING=OFF \
         -DWITH_ZLIB=ON \
         -Dutf8cpp_INCLUDE_DIR=/tmp/taglib-build/utfcpp-4.0.6/source \
         -DCMAKE_C_FLAGS="-fPIC" -DCMAKE_CXX_FLAGS="-fPIC" \
         >/dev/null 2>&1 \
    && make -j$(nproc) >/dev/null 2>&1 \
    && make install >/dev/null 2>&1 \
    && rm -f /tmp/taglib-build/utfcpp.tar.gz /tmp/taglib-build/taglib.tar.gz

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

# Install TagLib static libs into vendored location
RUN mkdir -p third_party/taglib/lib third_party/taglib/include \
    && cp /tmp/taglib-build/install/lib/libtag.a third_party/taglib/lib/ \
    && cp /tmp/taglib-build/install/lib/libtag_c.a third_party/taglib/lib/ \
    && cp /usr/lib/libz.a third_party/taglib/lib/ \
    && cp /tmp/taglib-build/install/include/taglib/tag_c.h third_party/taglib/include/ \
    && rm -rf /tmp/taglib-build

# Copy built frontend into web/dist so go:embed picks it up
COPY --from=frontend-builder /build/web/dist ./web/dist

# Accept version from build arg (since .git is excluded via .dockerignore)
ARG APP_VERSION=dev

# Build statically-linked binary with CGO (for FTS5 + native TagLib) and embedded frontend
RUN CGO_ENABLED=1 go build \
    -tags "embed_frontend fts5 native_taglib" \
    -ldflags="-s -w -linkmode external -extldflags '-static' -X main.version=${APP_VERSION}" \
    -o audiobook-organizer \
    .

# Stage 3: Minimal runtime image (scratch-compatible since binary is static)
# SHA pinned 2026-06-23 (alpine:3.24 manifest-list).
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

RUN apk add --no-cache ca-certificates tzdata ffmpeg \
    && addgroup -g 1000 audiobook \
    && adduser -D -u 1000 -G audiobook audiobook

WORKDIR /app

COPY --from=go-builder --chown=audiobook:audiobook /build/audiobook-organizer /app/

# Default data directory
RUN mkdir -p /data && chown audiobook:audiobook /data
VOLUME /data

USER audiobook

EXPOSE 8484

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8484/health || exit 1

ENTRYPOINT ["/app/audiobook-organizer"]
CMD ["serve", "--host", "0.0.0.0", "--db", "/data/audiobooks.pebble"]
