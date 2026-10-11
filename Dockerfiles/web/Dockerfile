FROM golang:1.24-alpine AS builder

RUN apk add upx

WORKDIR /go/src/github.com/infinimesh/core
COPY go.mod go.sum ./
COPY . .
RUN GOPROXY=https://proxy.golang.org,direct go mod download
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -buildvcs=false ./cmd/web
RUN upx ./web

FROM scratch
WORKDIR /
COPY --from=builder /go/src/github.com/infinimesh/core/web /web

LABEL org.opencontainers.image.source="https://github.com/infinimesh/core"

ENTRYPOINT ["/web"]
