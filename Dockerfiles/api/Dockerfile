FROM golang:1.26-alpine AS builder

RUN apk add upx

WORKDIR /go/src/github.com/infinimesh/core
COPY go.mod go.sum ./
COPY . .
RUN GOPROXY=https://proxy.golang.org,direct go mod download
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -buildvcs=false ./cmd/api
RUN upx ./api

# Download Healthcheck
WORKDIR /health
RUN wget https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/v0.4.14/grpc_health_probe-linux-amd64 -O check && chmod +x check

FROM scratch
WORKDIR /
COPY --from=builder /go/src/github.com/infinimesh/core/api /api
COPY --from=builder /health/check /check

HEALTHCHECK --start-period=5s CMD [ "/check", "-addr=:8000" ]

LABEL org.opencontainers.image.source="https://github.com/infinimesh/core"

ENTRYPOINT ["/api"]
