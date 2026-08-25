FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY webui ./webui
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/chatgpt-space-merge ./cmd/server

FROM alpine:3.21
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=builder /out/chatgpt-space-merge /app/chatgpt-space-merge
RUN mkdir -p /data && chown app:app /data
USER app
ENV APP_ADDR=0.0.0.0:18120 APP_DATA_DIR=/data
EXPOSE 18120
ENTRYPOINT ["/app/chatgpt-space-merge"]
