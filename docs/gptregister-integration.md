# 邮件管理接入 GPT Register

邮件管理新增 **GPT Register** 配置页和邮件账号列表的 **注册** 按钮。

## 配置及使用

填写平台根地址（也兼容以 `/open/v1` 结尾）和 API Key，保存配置。密钥需具备 `orders:write`、`orders:read`、`accounts:read`、`credentials:read` 权限。已保存的密钥不回显；留空表示保留，更换平台地址时必须重新提供密钥。

“测试连接 / 刷新余额”只调用 `/open/v1/me` 和 `/open/v1/products`，展示账户、可用积分、冻结积分、两种模式单价、注册开放状态和数量上限，不创建订单。此测试不代表已验证全部下单及凭据读取权限。`points` 已经是可用积分，不再次扣除 `frozen`。

勾选“导入后直接进入 Pro 管理”，新交付账号直接归属 Pro；否则进入邮件账号。该配置随任务保存，修改设置不改变已提交任务，也不自动启动开通、付款或团队流程。注册弹窗仅选择数量和是否获取 OAuth。

## 交付和恢复

- 成功交付一个就导入一个。OAuth 账号须包含完整 AT / RT；密码和 2FA 一并保存，完整 OAuth 对象另作加密留存。
- 邮箱已存在或有死号记录时跳过，保留原资料、管理归属和业务流程，可在导入明细查看原因。
- 本地先保存任务及请求标识，再用任务 ID 作为平台 `Idempotency-Key` 下单。丢失响应、重试或重启继续同一任务，不新建另一个注册订单。
- 平台地址、密钥和导入位置随任务加密保存。配置改动不会将已有订单转给别的平台；“继续原任务”允许更新同一平台的密钥，后台会先校验用户身份。
- 每项交付与本地账号在同一事务中保存去重记录。记录独立于邮件账号，删除邮件账号也不会再次导入该交付。
- 后台持久化同步，关闭页面仍继续执行；每轮最多获取 6 份新的凭据，任务轮询至少间隔 5 秒，请求串行节流。页面只在有活动任务时轮询，只有新增导入时才通知账号列表更新。
- 网络故障及 5xx 退避重试；429 遵循 Retry-After（至少 60 秒）；认证、权限或余额错误暂停并显示原因，修正后点“继续原任务”。平台 `review` 只同步原订单已交付结果，不重新注册。
- `completed/partial/failed/cancelled` 都是平台结束状态；本地完成同步后仍展示平台实际结果，不把注册失败显示为交付成功。显示最近 100 条任务及全部未完成任务，去重记录不随列表截断。

## 本地验证

```text
go test ./internal/gptregister ./internal/store ./internal/httpapi -run '^TestGPTRegister' -count=1
go test ./internal/store ./internal/httpapi -run 'Test(MailImport|ImportMailAccount|Mail.*Pro|Mail.*Scope)' -count=1
go vet ./internal/gptregister ./internal/store ./internal/httpapi
cd frontend
npm run check
npm run test:mail-import
npm run test:mail-pro-transfer
npm run test:mail-export
npm run build
```

`frontend/scripts/gptregister-preview.html` 为隔离浏览器模拟页面，所有 fetch 在本地拦截，不请求真实后端。在该页面执行 `check-gptregister-browser.js`，重新加载后再执行一次，验证提交响应丢失、页面刷新和原任务恢复，以及增量导入进度和明细。

接口依据本地 `gpt-register/src/platform/openapi.js`、`api.js`、`orders.js` 的公开客户 API；不调用其内部注册引擎。
