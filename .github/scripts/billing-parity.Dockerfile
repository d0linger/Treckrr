FROM golang:1.27.3-alpine3.24
RUN apk add --no-cache build-base postgresql16-client
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
