FROM golang:1.26-alpine AS builder

RUN apk add upx

WORKDIR /go/src/github.com/infinimesh/core
COPY go.mod go.sum ./
COPY . .
RUN GOPROXY=https://proxy.golang.org,direct go mod download
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -buildvcs=false ./cmd/mqtt-bridge
RUN upx ./mqtt-bridge

FROM scratch
WORKDIR /
COPY --from=builder /go/src/github.com/infinimesh/core/mqtt-bridge /mqtt-bridge

LABEL org.opencontainers.image.source="https://github.com/infinimesh/core"

ENTRYPOINT ["/mqtt-bridge"]
