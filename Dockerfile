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
# The OAuth runtime also has a Node Sentinel fallback, so Node.js and the full
# internal/codex_runtime package must be present in the image. A Docker image
# does not inherit Python packages or source files from the host.
RUN apk add --no-cache python3 py3-pip nodejs npm libstdc++ ca-certificates tzdata \
    && python3 -m pip install --break-system-packages --no-cache-dir curl_cffi pyotp \
    && python3 -c "import curl_cffi, pyotp; print('python protocol dependencies ok')"
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=builder /out/chatgpt-space-merge /app/chatgpt-space-merge
# These scripts are invoked using paths relative to /app by the Go backend.
COPY --from=builder /src/internal/protocol_login.py /app/internal/protocol_login.py
COPY --from=builder /src/internal/protocol_codex_oauth.py /app/internal/protocol_codex_oauth.py
COPY --from=builder /src/internal/protocol_proxy_probe.py /app/internal/protocol_proxy_probe.py
COPY --from=builder /src/internal/protocol_oauth_token.py /app/internal/protocol_oauth_token.py
# Bundle the local manager OAuth port and the runtime used by the other flows.
COPY --from=builder /src/internal/codex_runtime /app/internal/codex_runtime
COPY --from=builder /src/internal/test_protocol_codex_oauth.py /tmp/test_protocol_codex_oauth.py
RUN npm --prefix /app/internal/codex_runtime/manager_oauth ci --omit=dev --ignore-scripts \
    && PYTHONPATH=/app/internal/codex_runtime python3 -c "import manager_oauth.adapter, manager_oauth.sentinel_vm, manager_oauth.sms_providers; print('manager OAuth runtime ok')" \
    && node -e "require('/app/internal/codex_runtime/manager_oauth/openai_sentinel_token.cjs'); console.log('manager OAuth Node fallback ok')" \
    && PYTHONPATH=/app/internal/codex_runtime python3 /tmp/test_protocol_codex_oauth.py
RUN test -f /app/internal/codex_runtime/sentinel/sentinel-runner.js \
    && test -f /app/internal/codex_runtime/sentinel/sdk.js \
    && node --version \
    && PYTHONPATH=/app/internal/codex_runtime python3 -c "import config, config.codex, core.session, core.codex_oauth; print('codex protocol runtime ok')"
RUN mkdir -p /data && chown app:app /data
USER app
ENV TZ=Asia/Shanghai APP_ADDR=0.0.0.0:18120 APP_DATA_DIR=/data
EXPOSE 18120
ENTRYPOINT ["/app/chatgpt-space-merge"]
