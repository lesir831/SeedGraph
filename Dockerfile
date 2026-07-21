# syntax=docker/dockerfile:1.12

FROM --platform=$BUILDPLATFORM node:24-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY frontend/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS go-modules
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download \
    && go mod verify

FROM --platform=$BUILDPLATFORM go-modules AS backend
ARG TARGETOS
ARG TARGETARCH
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN --mount=type=cache,id=seedgraph-go-build-${TARGETOS}-${TARGETARCH},target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/seedgraph ./cmd/seedgraph

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 seedgraph \
    && adduser -S -D -H -u 10001 -G seedgraph seedgraph \
    && mkdir -p /app/web /data \
    && chown -R seedgraph:seedgraph /app /data
WORKDIR /app
COPY --from=backend --chown=seedgraph:seedgraph /out/seedgraph /app/seedgraph
COPY --from=frontend --chown=seedgraph:seedgraph /src/frontend/dist /app/web
ENV SEEDGRAPH_LISTEN_ADDR=:8080 \
    SEEDGRAPH_DATABASE_PATH=/data/seedgraph.db \
    SEEDGRAPH_WEB_DIR=/app/web
USER seedgraph
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O - http://127.0.0.1:8080/healthz >/dev/null || exit 1
STOPSIGNAL SIGTERM
ENTRYPOINT ["/app/seedgraph"]
