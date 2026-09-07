FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY webui ./webui
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/chatgpt-space-merge ./cmd/server

FROM alpine:3.21
# Protocol login/OAuth flows are executed by the Go service through Python.
# Install them in the image instead of relying on packages from the host.
RUN apk add --no-cache python3 py3-pip libstdc++ \
    && python3 -m pip install --break-system-packages --no-cache-dir curl_cffi
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=builder /out/chatgpt-space-merge /app/chatgpt-space-merge
# These scripts are invoked using paths relative to /app by the Go backend.
COPY --from=builder /src/internal/protocol_login.py /app/internal/protocol_login.py
COPY --from=builder /src/internal/protocol_codex_oauth.py /app/internal/protocol_codex_oauth.py
RUN mkdir -p /data && chown app:app /data
USER app
ENV APP_ADDR=0.0.0.0:18120 APP_DATA_DIR=/data
EXPOSE 18120
ENTRYPOINT ["/app/chatgpt-space-merge"]
