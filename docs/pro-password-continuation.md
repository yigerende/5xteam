# Space 所有 OAI 登录统一优先密码

2026-11-18：密码模式先提交密码、按上游要求完成 2FA；随后仍要求邮箱验证码时，才调用原收码来源及验证协议。用户授权推送并更新 Space merge / user 两套正式环境。

## 覆盖入口

| 入口 | 实际共用路径 |
| --- | --- |
| Pro 全自动第一步、手动登录和其重试 | ContinuousProLogin / ProjectProtocolLogin |
| 临时 AT、重新获取 AT、429 重建登录 | adapter.run → ProjectProtocolLogin |
| Codex OAuth、轮转登录和重新授权 | protocol_codex_oauth.py → ProjectProtocolLogin |
| SIWC 授权前登录 | ProjectProtocolLogin → 原 SIWC 同会话授权 |
| 额度首次重登、凭据失效后重登 | pro_siwc_quota.go → executeOpenAILoginContext → ProjectProtocolLogin |
| OAI 确认、删除前 Help 准备、原代理重登及重试 | pro_oai_confirmation.go / help_confirmation.py → ProjectProtocolLogin |
| 合并团队登录、个人套餐兜底登录 | TeamProtocolLogin / PersonalLogin → ProjectProtocolLogin |

扫描全部 Go 的 Python 执行入口及 OAI 密码接口调用。protocol_login.py 的邮箱专用辅助无密码参数、无现行业务调用；注册新账号的邮箱确认不属于密码登录。protocol_oauth_token.py 只交换/刷新 Token，不提交登录密码。core 旧引擎没有当前密码业务入口引用。不改变这些协议。

## 行为

共享 password_first.py 接收明确的 passwordless_login 邮箱页，在选定密码模式、密码尚未提交时，用原客户端 GET client_auth_session_dump 核对账号和模式，再执行原 password/verify / MFA。保留 Cookie、设备标识和代理；普通直接密码登录不请求 dump，也不显示切换密码提示。合并初始邮箱页不再因缺少收码链接提前终止。

密码模式不在登录前访问邮箱收码链接；只有后置邮箱挑战才开启取码，继续原等待、轮询和提交协议。显式邮箱模式保留发码前旧码基线。密码模式因没有前置基线，从确需邮箱验证时读当前验证码，最终始终由上游校验。密码、TOTP 错误和身份不一致立即失败，不降级逃过验证；邮箱验证后仍返回密码/邮箱页不会被误报为登录成功。MFA 后邮箱页仅提供 page type 时补入固定官方路径，并沿原会话取码。

个人套餐核验不再在等待邮箱码日志处强行终止。需要邮箱码而缺来源仍明确失败；原团队/个人身份、空间、套餐验证保留。每个新登录对象重置密码提交状态，重试重新采用相同规则。密码/TOTP/邮箱的实际验证方式记录完整。

## 验证与发布

本地完整 Python 协议147项通过，包含20组 AT/OAI/团队/个人/SIWC 入口矩阵、普通密码无切换、密码与MFA后邮箱、MFA仅page响应、无收码链接、密码/身份/网络/2FA错误、重复挑战、429重新建会话及代理隔离。保留原Pro连续客户端、付款防重、已付款恢复、原上游源码AST一致性测试。具体Go及Linux镜像验证与发布版本记录在上级生产runbook。

测试使用隔离假上游，不实际收码、付款、退款、发Help消息或重跑旧单。只发布登录适配及测试，不改原上游核心、Go业务、数据库、配置、主备/重试预算、付款核实和后续调度。

Go 相关登录选择、全自动/重试/已付款恢复、额度/原代理、OAI、合并重试与死号边界回归通过（73.708秒）。额外发现旧 TestProOAuthLoginCompletionAndProgress/dead 断言要求已合并账号仍标死号，与现行 CanMarkProDead 排除已合并规则冲突；未修改基线亦可重现，同本次Python修复无关，未改业务规则或该旧测试。
