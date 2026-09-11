# Codex OAuth 移植记录

参考的是本机 gpt-account-manager/server.py 实际运行的入口：

`run_login_for_credential_mode → run_chatgpt_login_codex_then_web → run_chatgpt_login_with_protocol → ChatGPTProtocolLogin.login`

参考文件的 SHA-256 和逐方法 AST 校验值保存在 `parity.json`。没有使用旧 refer_oauth.py，也没有从外部项目加载代码、调用 manager 服务或把 OAuth 回调交给 CPA。

## 已迁移的协议行为

- chrome131 请求指纹、独立 CookieJar、紧凑 JSON 请求体、逐跳处理重定向。
- OAuth authorize 和 `/api/oauth/oauth2/auth` 两个入口；没有登录 Cookie 时返回结构化可重试错误，不再使用 BrowserSession 的 403 熔断冷却。
- 同一请求的网络重试、400 invalid_auth_step 的重新初始化、完整 OAuth 轮次重试。一个轮次始终固定一个代理。
- Sentinel Python 路径及 Node 备用路径，使用项目内的 sentinel_vm 和 Node helper。
- 密码、邮箱 OTP、TOTP、直接 consent、passkey/额外挑战失败分支。
- 发码前旧码基线、接码链接等待和轮询、累积已见邮件与旧码回退；直接链接和已知邮箱适配器的解析方法。
- add-phone、通道选择、短信验证、号码拒绝后换号（最多 3 个）、限流和会话失效的分类；实时平台申请、扣卡、取消/完成和退款回池。
- 账号选择、工作区、组织及项目选择、Cookie 解码、OAuth callback/state/PKCE、token 交换的网络/5xx 重试。

Team 第三步、邮件管理单个/批量登录、自动轮转、401 重登和手动重登都经过 Go 的 `executeCodexOAuth`，使用同一入口。Pro 的人工授权链接仍保留人工登录；其 code→token 交换使用同一 chrome131 和请求头，交换期间固定代理，不会转成自动邮箱登录。单独刷新 RT 的既有业务调度保留。

## 明确保留的本项目接入差异

1. CPA 托管 OAuth 的分支被删除，state、PKCE 和 token 交换都在本项目完成。
2. 日志接本项目诊断事件和现有异步日志存储；只记录 Cookie 名称/域/路径及凭证存在性，不记录值。
3. 账号及接码配置接本项目 Go 数据库。Python 与所属 Go 任务通过标准输入输出传递请求，无额外 HTTP 服务。号码池选择、活动租用和卡密池扣减在 Go 端协调并发；任务退出清理租用。
4. 保留本项目明确死号后的结构化返回。该信号直接终止协议流程，供现有 Go 移出空间逻辑处理，普通 403/429 不判死号。
5. 按用户先前要求，不恢复参考项目的“两次出口 IP 不一致就拒绝”检查。出口信息查询失败只作诊断，不把已经通过入口可达性检测的代理直接拒绝。
6. Node 备用路径初始化已配置代理失败时禁止直连；SDK 缓存位于可写临时目录，兼容 Docker 非 root 用户。
7. 接码账户/邮箱数据由本项目已有字段提供，未新增参考项目的独立邮箱管理、外部数据库和密码修改页面。
8. 邮箱验证码登录也保留已有 TOTP 密钥；验证码验证后若返回 `mfa_challenge`，在手机验证分支之前复用原有 `complete_totp_challenge`。未触发 MFA 时不提交 TOTP，密码登录选择及 OAuth 后续流程不变。`complete_modern_login` 因这一分支扩展列入 adapted，TOTP 请求方法本身保持参考实现。

因此不能把整个服务器描述为逐字相同：上述是必要接入边界。与参考源码 AST 相同的方法/辅助函数列在 `parity.json` 的 identical_ast_sha256 中；其余移植修改显式列在 adapted 字段中。

## 验证

`python -m unittest discover -s internal -p 'test_protocol*.py' -v`

测试替换底层 HTTP 返回，不跳过 bootstrap、CookieJar、PKCE、OTP 或 consent。覆盖授权入口 403→备用入口、两个入口均失败、网络重试、invalid_auth_step、邮箱旧码、密码/TOTP/直接 consent、死号与普通错误、短信换号/限流、token 重试以及日志脱敏。逐方法源码校验防止后续无意偏离。

`go test ./...`

包含结构化可重试结果、Go/Python 标准流交互、超时终止、并发号码租用与绑定复用测试。Dockerfile 安装 Python、curl_cffi、pyotp、Node 和锁定版本的 undici，并在构建时验证运行时模块。

本次验证使用模拟账号和 HTTP 返回，未使用真实账号授权或购买短信。上游是否接受某个实际账号/代理仍需要对应的真实授权结果验证。
