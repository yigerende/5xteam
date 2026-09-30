# Pro 开通供应商与 Pro 50x

Pro 全自动配置中的「开通供应商」选择 GPTPay · Tokenseek 或 GPTPay · CMSNav；手动开通、全自动开通及定时开通均使用保存的选择。两家供应商分别保存 API 地址、加密密钥、套餐、支付国家，切换不会沿用另一家的密钥。银行卡名额上限与登录会话保留时间共用。

| 供应商 | 默认 API 地址 | 支持套餐 |
| --- | --- | --- |
| Tokenseek | `https://gptpay.tokenseek.app/api/v1` | Pro 5x、Pro 20x |
| CMSNav | `https://gptpay.cmsnav.com/api/v1` | Pro 5x、Pro 20x、Pro 50x |

Tokenseek 保持现有协议。CMSNav 依据 <https://gptpay.cmsnav.com/docs> 和 <https://gptpay.cmsnav.com/api/openapi.json>（2026-09-30）接入：

- `GET /api/catalog` 公开查询国家、货币、套餐积分价格及可用状态，不发送 API Key。切换到 CMSNav 或打开其配置时自动读取，也可点击「刷新国家与套餐」。支付国家通过下拉目录选择，显示国家名称、代码及币种；Pro 套餐显示积分价格，暂停的套餐不可选。目录请求失败显示重试提示，保留原选择，不自动换国家或套餐。
- 本地 `GET /api/gptpay/catalog?provider=cmsnav&url=...` 使用当前填写的供应商地址，未保存配置、未填写密钥也可查询；仅打开配置或手动刷新时读取，不增加后台轮询。修改 API 地址后自动重新读取，过期响应不能覆盖新目录。
- `GET /customer/wallet` 查询账户；读取 `data.balance`、`data.held`、`data.prices`、`data.membership`。
- `POST /customer/recharges` 下单；内部 `pro5/pro20/pro50` 对应 `productCode=pro5x/pro20x/pro50x`。顶层传 `country`（默认 US，可改 PH 等两位国家代码）、`cancelRenewal=true`、`session` 和 `card`。
- `session` 保留已保存 Web Session 的字段（含 `user.email`、`account.id`、`accessToken`），过滤本地附加的 RT、ID Token、Session Cookie 等凭据。Session 必须与本次 AT 和邮箱匹配；只有 AT 时先获取临时 AT，或使用全自动开通的第一步登录。
- `card` 传 `cardNumber`、`expMonth`、`expYear`、`cvv`。密钥仅用 `Authorization: Bearer` 请求头。
- 创建时设置稳定的 `Idempotency-Key`；保存 `data.taskId`（兼容 `orderId/localOrderId`）。`reserved` 表示处理中。
- `GET /customer/orders/{taskId}` 查询订单；手动批量查询最多 50 条、并发最多 4 个请求。按查询 ID 关联没有返回 ID 的状态响应。
- 开通状态与取消续费状态独立；`progress.rechargeStatus=success` 表示开通成功，取消续费失败不会阻止后续授权。

每笔订单加密保存供应商、地址、密钥、国家和完整下单参数。修改配置后，已提交订单仍用原快照查询/重试；网络超时、5xx 或无法确认的响应保留「提交待确认」，只允许重试原订单。旧订单默认属于 Tokenseek，订单导入导出也保留供应商快照。

验证采用模拟 HTTP 供应商与隔离浏览器页面，覆盖两家供应商、50x 参数、密钥隔离与重启持久化、事务回滚、原订单幂等重试、并发查询、连续会话自动流程、取消续费失败和低额度等待；不会向供应商提交真实付费订单。
