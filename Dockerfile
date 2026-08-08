FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/forum-server ./cmd/server

FROM alpine:3.21
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=builder /out/forum-server /app/forum-server
RUN mkdir -p /app/logs && chown -R app:app /app
USER app
EXPOSE 8080
ENTRYPOINT ["/app/forum-server"]
