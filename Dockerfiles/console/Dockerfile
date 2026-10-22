FROM node:20 AS console-builder

ARG INFINIMESH_VERSION_TAG
ENV INFINIMESH_VERSION_TAG=$INFINIMESH_VERSION_TAG

ADD console /console
WORKDIR /console

RUN npm install -g pnpm@9.15.9
RUN pnpm install --frozen-lockfile && pnpm build

# Build server
FROM golang:1.27.3-alpine AS builder

RUN apk add upx

WORKDIR /go/src/github.com/infinimesh/core

COPY go.mod go.sum ./
RUN GOPROXY=https://proxy.golang.org,direct go mod download

COPY ./cmd/console ./cmd/console
COPY ./pkg/observe ./pkg/observe
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -buildvcs=false ./cmd/console
RUN upx ./console

# Switch to the scratch image
FROM scratch

COPY --from=console-builder /console/dist/ /app
COPY --from=builder /go/src/github.com/infinimesh/core/console /console

EXPOSE 80

LABEL org.opencontainers.image.source="https://github.com/infinimesh/core"

# Run thttpd
ENTRYPOINT ["/console"]
