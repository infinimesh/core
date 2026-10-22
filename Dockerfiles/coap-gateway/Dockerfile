FROM golang:1.27.3-alpine AS builder

WORKDIR /go/src/github.com/infinimesh/core
COPY go.mod go.sum ./
COPY . .
RUN GOPROXY=https://proxy.golang.org,direct go mod download
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -buildvcs=false ./cmd/coap-gateway

FROM scratch
WORKDIR /
COPY --from=builder /go/src/github.com/infinimesh/core/coap-gateway /coap-gateway

LABEL org.opencontainers.image.source="https://github.com/infinimesh/core"

ENTRYPOINT ["/coap-gateway"]
