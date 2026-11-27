# Toolbox image for developing zdev: the Go toolchain lives here, not on the host.
FROM golang:1.27-trixie

# A non-root user owns the synced source and the Go caches, so builds don't
# leave root-owned files behind and `zdev claude` doesn't run as root.
RUN useradd --create-home --uid 1000 --shell /bin/bash dev \
 && mkdir -p /go/pkg/mod /home/dev/.cache/go-build \
 && chown -R dev:dev /go /home/dev

USER dev
# gopls for Claude's Go language server plugin in `zdev claude` sessions.
# The module cache is dropped: the go_mod volume replaces /go/pkg/mod anyway.
RUN go install golang.org/x/tools/gopls@v0.24.0 && go clean -modcache
# Static binaries, like the release build.
ENV CGO_ENABLED=0
WORKDIR /app
