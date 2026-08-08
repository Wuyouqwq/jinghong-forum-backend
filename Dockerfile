FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/forum-server ./cmd/server

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=builder /out/forum-server /app/forum-server
RUN mkdir -p /app/logs && chown -R app:app /app
USER app
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 CMD wget -qO- "http://127.0.0.1:${PORT:-8080}/readyz" >/dev/null || exit 1
ENTRYPOINT ["/app/forum-server"]
