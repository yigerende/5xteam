# 序付供应商对接

对照 xufupython 客户 API 1.2.0（2026-11-04）。Space 使用 `POST /api/v1/orders` 创建 `subscription` 订单，再按保存的供应商订单 ID 查询详情。首次全自动登录取得的完整网页 Session 会保存在加密订单快照中；不增加登录，不强制调用 session/inspect。本地 OAuth 附加字段不发送给供应商。

新订单的套餐映射为 `pro5 → pro5x`、`pro20 → pro20x`，套餐列表和查询结果使用相同映射。Space 内部配置仍为 pro5/pro20；新增订单快照保存实际 product_id，重试不因代码更新而改变请求内容。升级前没有此字段的旧快照继续按原值 pro 重放，旧订单详情的 pro 仍识别为 Pro 20x。只读兼容旧订单不代表新套餐列表可以继续使用 pro。

默认明确传 `keep_renewal=false`。仅当 `payment.state=succeeded` 且 `order.activation=active` 时将开通标为成功，立即继续原有 RT/AT 和推送阶段。取消续费单独跟踪，失败不撤销开通成功、不重新付款。依据公开 order.status 的 queued/processing/awaiting_payment 识别办理中，旧版 automatic_running=true 仍兼容；不会因为新版缺少该字段就把办理中的临时结果判为终止。

创建接口返回 HTTP 429 时，优先使用 Retry-After（支持秒数和 HTTP 日期），缺省等待 60 秒，最多自动重试 3 次。次数和下次时间保存到原订单，使用同一幂等键和同一加密快照；不能消耗换号预算。查询限流同样按 Retry-After 等待。同一 Space 进程内，相同供应商地址/API Key 共用限流等待；不同进程之间仍以供应商账户级限制为准。重试耗尽保留原任务、原订单及付款不确定性。

xufupython 1.2.0 不提供 `payment/check`，也不返回 can_check。缺少该能力时，开通订单列表和开通弹窗不展示“核实原单”，仍可“查询状态”；旧页面调用该操作也先读取最新能力，未明确允许则拒绝，不向序付发送 POST、不留下新的核实占用。只有明确返回 can_check=true 的旧版供应商保留兼容入口。独立关闭续费接口不充当付款核实接口。

余额接口的 balance 为总余额，frozen 为冻结服务费，Space 可用余额显示 balance - frozen，单位仍为 cent_R。HTTP 错误只匹配已知的无敏感值校验信息，区分余额不足、Session 过期/无效、银行卡参数、套餐下架、接口未部署、账单及代理配置等；任意原始报错不直接回显。错误分类不改变原有扣款不确定性保护，404/409/5xx 的创建响应仍保留原单，不能据此重新付款。

详情展示公开错误码、说明和最近 20 条事件，每项说明最多 1000 字符，保存前隐藏 Session、Cookie、AT、API Key、卡号和 CVC 等已知敏感值。付款金额明确区分报价与已确认实收。页面不会返回加密订单快照或付款凭据。

回归使用本机模拟供应商和临时数据库，覆盖完整 Session、pro20x 与旧快照重放、可用余额、429 等待/重启/预算、公开状态跟踪、缺失核实能力、取消失败继续全自动、错误分类、诊断脱敏和页面渲染；不使用真实付款作为回归测试。上线前另须确认序付已部署 POST /api/v1/orders，网页付款可用或 payment_connected=true 不能单独证明客户创建接口已上线。
