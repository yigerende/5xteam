# 序付供应商对接

对照公开 API 1.6.5（2026-10-31）。Space 使用 `POST /api/v1/orders` 创建 `subscription` 订单，再按保存的供应商订单 ID 查询详情。首次全自动登录取得的完整网页 Session 会保存在加密订单快照中；不增加登录，不强制调用 session/inspect。本地 OAuth 附加字段不发送给供应商。

默认 `keep_renewal=false`。仅当 `payment.state=succeeded` 且 `order.activation=active` 时将开通标为成功，立即继续原有 RT/AT 和推送阶段。取消续费单独跟踪，失败不撤销开通成功、不重新付款。序付仍返回 `automatic_running=true` 时继续查询，不把暂时的 review/action_required 直接判成人工终止。

创建接口返回 HTTP 429 时，优先使用 Retry-After（支持秒数和 HTTP 日期），缺省等待 60 秒，最多自动重试 3 次。次数和下次时间保存到原订单，使用同一幂等键和同一加密快照；不能消耗换号预算。查询限流同样按 Retry-After 等待。同一 Space 进程内，相同供应商地址/API Key 共用限流等待；不同进程之间仍以供应商账户级限制为准。重试耗尽保留原任务、原订单及付款不确定性。

开通订单列表和开通弹窗新增“核实原单”。调用前重新读取详情，尊重 `can_check` 和 `automatic_running`；旧版缺少 can_check 时仅对 review/action_required 提供入口。此操作仅调用已有订单的 `payment/check`，受理不等于成功，随后继续查询。发送前持久保存核实标记；响应丢失、服务重启时先查询结果，不自动重复核实请求或创建付款。

详情展示公开错误码、说明和最近 20 条事件，每项说明最多 1000 字符，保存前隐藏 Session、Cookie、AT、API Key、卡号和 CVC 等已知敏感值。付款金额明确区分报价与已确认实收。页面不会返回加密订单快照或付款凭据。

回归使用本机模拟供应商和临时数据库，覆盖完整 Session、原单幂等、429 等待/重启/预算、自动状态跟踪、核实权限及未知响应、取消失败继续全自动、诊断脱敏和页面渲染；不使用真实付款作为回归测试。
