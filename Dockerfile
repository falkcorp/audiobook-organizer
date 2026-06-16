# file: Dockerfile
# version: 2.5.0
# guid: audiobook-organizer-dockerfile-production

# Multi-stage production Dockerfile for audiobook-organizer
# Builds React frontend, embeds it into a statically-linked Go binary with
# CGO enabled (for SQLite FTS5 support), produces a minimal container.

# Stage 1: Build frontend
FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend-builder

WORKDIR /build/web

COPY web/package*.json ./
RUN npm ci --prefer-offline --no-audit

COPY web/ ./
RUN npm run build

# Stage 2: Build Go application with embedded frontend
# Uses native platform (no cross-compile) so CGO works without cross-toolchain.
FROM golang:1.26-alpine AS go-builder

WORKDIR /build

ENV GOEXPERIMENT=jsonv2
RUN apk add --no-cache git gcc g++ musl-dev sqlite-dev ca-certificates tzdata \
    cmake make curl zlib-dev zlib-static

# Build TagLib static libraries for native CGO bindings
RUN set -ex \
    && mkdir -p /tmp/taglib-build/install/lib /tmp/taglib-build/install/include \
    && cd /tmp/taglib-build \
    # utfcpp (header-only taglib dependency)
    && curl -sL https://github.com/nemtrif/utfcpp/archive/refs/tags/v4.0.6.tar.gz | tar xz \
    # taglib (uses system zlib from apk)
    && curl -sL https://github.com/taglib/taglib/releases/download/v2.0.2/taglib-2.0.2.tar.gz | tar xz \
    && mkdir -p build && cd build \
    && cmake ../taglib-2.0.2 \
         -DCMAKE_INSTALL_PREFIX=/tmp/taglib-build/install \
         -DBUILD_SHARED_LIBS=OFF -DBUILD_EXAMPLES=OFF -DBUILD_TESTING=OFF \
         -DWITH_ZLIB=ON \
         -Dutf8cpp_INCLUDE_DIR=/tmp/taglib-build/utfcpp-4.0.6/source \
         -DCMAKE_C_FLAGS="-fPIC" -DCMAKE_CXX_FLAGS="-fPIC" \
         >/dev/null 2>&1 \
    && make -j$(nproc) >/dev/null 2>&1 \
    && make install >/dev/null 2>&1

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
FROM alpine:3.24

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
