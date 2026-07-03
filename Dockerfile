# syntax=docker/dockerfile:1

# ---------- Build stage ----------
FROM golang:1.24-alpine AS build

WORKDIR /src

# Resolve dependencies. go.sum is generated here so the repo need not ship it.
ENV GOFLAGS=-mod=mod
COPY go.mod ./
COPY . .
RUN go mod tidy

# Build a static binary. CGO is off so the resulting binary is self-contained.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/treckrr ./cmd/treckrr

# ---------- Runtime stage ----------
FROM alpine:3.20

# Non-root user & CA certs (for completeness; app talks only to local DB).
RUN apk add --no-cache ca-certificates tzdata wget \
	&& adduser -D -u 10001 treckrr
ENV TZ=Europe/Vienna

WORKDIR /app
COPY --from=build /out/treckrr /app/treckrr

USER treckrr
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 \
	CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/treckrr"]
