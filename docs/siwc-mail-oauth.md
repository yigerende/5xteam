# 邮件管理 SIWC RT / AT

邮件管理的更多操作中选择“获取 SIWC RT / AT”，也可勾选账号后批量获取。
沿用接口设置的代理及原 OAuth 的登录方式、验证码/2FA、短信处理和重试。
成功后在“查看、复制 / 导出 AT”查看或复制 SIWC AT / RT，点击“导出 SIWC JSON”可导入 sub2api SIWC。

SIWC 与普通 Codex、ChatGPT Web 凭证独立加密保存。获取 SIWC 不触发 Pro 全自动、推送或 vm2api 重新授权。
导出包含已签发的 client_id、ext_agent_host_id、auth_mode、授权范围、有效期、已验证身份及 ID Token；普通 CPA/Sub2 导出保持原格式。

## 协议

- Space 在同一个登录任务中生成并保管 PKCE verifier、state 和 nonce，授权入口为 `https://auth.openai.com/api/accounts/authorize`。
- 登录阶段使用 `ProjectProtocolLogin` 的原方法；SIWC 仅覆盖授权入口、授权后的跳转和 Token 兑换。
- 按页面需要选择账号，读取 `client_auth_session_dump`，使用该会话的 session_id 调用 `dynamic-agent-clients/consent/grant`。
- 本地和 s2 接口方式共用上述 code 获取链路。进入 SIWC 授权后，`session/select`、`client_auth_session_dump`、`dynamic-agent-clients/consent/grant` 各自获取新的 `authorize_continue` Sentinel，不复用密码/邮箱登录阶段的校验令牌。原登录客户端、Cookie、设备标识和代理会话保持；登录阶段为密码优先读取的 dump 不受此逻辑影响。
- Sentinel 生成失败在该请求发送前停止，记录 `siwc_sentinel_failed`；不带空令牌或旧令牌继续，不额外启动整轮重登。日志仅记录端点、flow、生成成功/失败及耗时，不记录令牌内容。
- 捕获 `http://127.0.0.1:1456/auth/callback` 的 code/state/client_id，不请求本机回调端口。
- 使用本次 verifier 和签发 client_id 调用 `/api/accounts/oauth/token`，携带 resource `https://api.openai.com/v1`。
- 校验回调、OpenID JWKS 签名、issuer/audience/expiry/nonce 和目标邮箱后才保存；任务轮询不返回 Token。
- 宿主标识按安装和邮箱稳定生成，成功保存后复用。未授予 `chatgpt.tokens.use.direct` 时页面明确显示计划用量未授权。

## 验证

- `python internal/test_protocol_siwc.py`：真实登录方法＋HAR 结构的模拟 HTTP，覆盖邮箱 OTP、密码/TOTP、账号选择、两种来源逐步刷新授权 Sentinel、同会话/设备/代理、生成失败不发送请求、非 SIWC 端点不受影响、PKCE、回调拒绝、真实 RSA 签名校验、Token 重试和日志脱敏。
- `python internal/test_protocol_codex_oauth.py`：普通登录回归。
- `go test ./internal/httpapi -run 'TestMailSIWC|TestSIWC'`：隔离保存、身份校验、查看导出、普通凭证更新及重新导入后的保留。
- 启动 Vite 后，在 `scripts/mail-management-preview.html` 执行 `scripts/check-mail-siwc-browser.js` 检查实际 UI。
- Docker 构建运行 Go 全套测试和两个 Python 测试文件。模拟测试不等同于实际账号授权成功。
