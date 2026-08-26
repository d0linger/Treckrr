# syntax=docker/dockerfile:1

# ---------- Build stage ----------
# Base images are pinned by DIGEST, not just by tag. A tag is mutable: alpine:3.24
# is rebuilt upstream whenever a package inside it is patched, so the same tag
# yields different content over time and two builds of identical source produce
# different images. Pinning makes a build reproducible and a rollback exact, and
# means a poisoned upstream tag cannot enter silently.
#
# The trade-off is deliberate: base security patches no longer arrive invisibly,
# they arrive as a Dependabot PR (the docker ecosystem updates the digest and
# keeps the tag comment in sync). That only works if those PRs get merged —
# pinning without that discipline freezes a known-vulnerable base, which is worse
# than following the tag. The weekly image scan in sbom.yml is the backstop that
# makes a stale pin visible.
FROM golang:1.27.0-alpine3.24@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build

WORKDIR /src

# Dependencies in their own layer, from the COMMITTED go.mod/go.sum. Two reasons
# this is not `go mod tidy` over the full source tree:
#   - Integrity: tidy rewrites go.mod/go.sum, so a missing or wrong checksum is
#     silently added rather than failing the build. -mod=readonly (Go's default,
#     stated here so it cannot be lost to a stray GOFLAGS) makes the committed
#     checksums an enforced gate; `go mod verify` re-checks the downloaded
#     module cache against them.
#   - Caching: only go.mod/go.sum invalidate this layer, so a source-only change
#     no longer re-resolves and re-downloads the whole module graph.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

# Build a static binary. CGO is off so the resulting binary is self-contained.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -mod=readonly -ldflags="-s -w" -o /out/treckrr ./cmd/treckrr

# ---------- Runtime stage ----------
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# Non-root user & CA certs (for completeness; app talks only to local DB).
# postgresql16-client provides pg_dump/pg_restore for encrypted backups; the
# major version must match the Postgres server (postgres:16-alpine).
# `apk upgrade` FIRST, and this is not optional hygiene. The digest pin above
# fixes the starting layer, which means the packages baked into it stay frozen at
# whatever Alpine shipped when that layer was built — while the Alpine repository
# keeps publishing security fixes for them. Without this the image ships
# known-vulnerable openssl for as long as the pin stands: the first run of the
# image scan caught exactly that, six fixed HIGH CVEs across libcrypto3, libssl3,
# libpq and postgresql16-client.
#
# Pinning and upgrading are complements, not alternatives: the pin makes the base
# layer reproducible, the upgrade makes the shipped packages current. Doing only
# the first is strictly worse than following the tag.
RUN apk upgrade --no-cache \
	&& apk add --no-cache ca-certificates tzdata wget postgresql16-client \
	&& adduser -D -u 10001 treckrr
ENV TZ=Europe/Vienna

# Provision the backup dir owned by the non-root app user. A named volume
# mounted here inherits this ownership on first creation, so uid 10001 can
# write dumps; without this, os.MkdirAll("/backups") / writing dumps fails
# with EACCES on a root-owned mount.
RUN mkdir -p /backups && chown treckrr:treckrr /backups
VOLUME ["/backups"]

WORKDIR /app
COPY --from=build /out/treckrr /app/treckrr
# So `docker exec treckrr-app treckrr restore …` resolves the binary.
ENV PATH="/app:${PATH}"

USER treckrr
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 \
	CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/treckrr"]
