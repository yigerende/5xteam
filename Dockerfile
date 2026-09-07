FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY webui ./webui
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/chatgpt-space-merge ./cmd/server

FROM alpine:3.21
# Protocol login/OAuth flows are executed by the Go service through Python.
# The OAuth runtime also executes sentinel-runner.js, so Node.js and the full
# internal/codex_runtime package must be present in the image. A Docker image
# does not inherit Python packages or source files from the host.
RUN apk add --no-cache python3 py3-pip nodejs libstdc++ ca-certificates tzdata \
    && python3 -m pip install --break-system-packages --no-cache-dir curl_cffi pyotp \
    && python3 -c "import curl_cffi, pyotp; print('python protocol dependencies ok')"
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=builder /out/chatgpt-space-merge /app/chatgpt-space-merge
# These scripts are invoked using paths relative to /app by the Go backend.
COPY --from=builder /src/internal/protocol_login.py /app/internal/protocol_login.py
COPY --from=builder /src/internal/protocol_codex_oauth.py /app/internal/protocol_codex_oauth.py
# protocol_codex_oauth.py imports config/core as top-level modules and invokes
# sentinel-runner.js from this runtime tree. Keep the complete package.
COPY --from=builder /src/internal/codex_runtime /app/internal/codex_runtime
RUN test -f /app/internal/codex_runtime/sentinel/sentinel-runner.js \
    && test -f /app/internal/codex_runtime/sentinel/sdk.js \
    && node --version \
    && PYTHONPATH=/app/internal/codex_runtime python3 -c "import config, config.codex, core.session, core.codex_oauth; print('codex protocol runtime ok')"
RUN mkdir -p /data && chown app:app /data
USER app
ENV APP_ADDR=0.0.0.0:18120 APP_DATA_DIR=/data
EXPOSE 18120
ENTRYPOINT ["/app/chatgpt-space-merge"]
