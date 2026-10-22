FROM golang:1.27-alpine AS builder

RUN apk add upx

WORKDIR /go/src/github.com/infinimesh/core
COPY go.mod go.sum ./
COPY . .
RUN GOPROXY=https://proxy.golang.org,direct go mod download
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -buildvcs=false ./cmd/gateway
RUN upx ./gateway

FROM scratch
WORKDIR /
COPY --from=builder /go/src/github.com/infinimesh/core/gateway /gateway

LABEL org.opencontainers.image.source="https://github.com/infinimesh/core"

ENTRYPOINT ["/gateway"]
