# EJH 外币 API 文档对照（2026-10-14）

2026-11-07 新版签名核对见 [EJH 双重验签全量对照](ejh-rsa-api-coverage.md)，下文为历史业务字段核对。

范围：官方 /docs/zh 外币文档全部菜单页面，以及同站卡列表新旧两份页面；不包含单独的 /docs-x 产品。文档核对不是生产接口验收或全面安全审计。

## 币种修正

- [官方币种表](https://www.ejhcard.com/docs/zh/appendix/currencies.html)是静态字典，没有公布币种列表查询 method。
- 已完整录入两侧表格共 157 个代码、中文名称、国家/地区，按代码排序；保留 CNH 及 ANG、HRK、SLL、STD、ZWL 等供应商仍列出的代码。
- 开卡模板的刷卡币种、结算币种，以及人工回款核实币种均使用同一个原生下拉组件。保存和开卡提交的仍是代码，不提交中文标签。
- 未收录的历史值作为“原值，文档未收录，请核实”显示，不自动换成 USD；不改变后端历史记录读取及三位币种格式校验。
- 账户余额 abroadList 展示账户币种，但不能据此推断全部卡类型权限。字典选项不等于账户/卡类型支持范围；100011、100012、100029 等错误仍需按卡台授权处理。

## 已核对页面与当前实现

| 文档页面 | 文档要点 / 本地情况 |
| --- | --- |
| [产品服务](https://www.ejhcard.com/docs/zh/index.html) | 外币文档导航与范围。 |
| [流程](https://www.ejhcard.com/docs/zh/access/flow.html) | 图片说明开卡额度校验、卡组织处理、销卡返回；不是订阅退款接口。 |
| [请求参数及示例](https://www.ejhcard.com/docs/zh/access/params-examples.html) | 已使用 /api/v3/card、appKey/param 加密信封、秒级 timestamp、v=3.0；文档容差 10 分钟。旧 ejiehui.com 地址弃用。 |
| [获取认证](https://www.ejhcard.com/docs/zh/api/auth.html) | 已使用 /api/v3/authenticate、appKey、Authorization 原始 token；validity 按毫秒时长处理。 |
| [开卡](https://www.ejhcard.com/docs/zh/api/create-card.html) | card.open.info 已接入模板/开卡页；activeDate 晚于当天，MCC、使用方式必填；可选订单日期说明与示例存在矛盾，未擅自转换格式。 |
| [销卡](https://www.ejhcard.com/docs/zh/api/cancel-card.html) | card.cancel.info 已接入确认操作；供应商限制已激活未使用卡；不是账号退款。 |
| [开卡查询](https://www.ejhcard.com/docs/zh/api/card-query.html) | card.query.info 已接入详情刷新，敏感卡信息按需查看。 |
| [账户余额](https://www.ejhcard.com/docs/zh/api/balance-query.html) | account.balance.info 已读取 abroadList/currencyName；余额快照由手动查询更新，旧 financeBalance/toBeRecorded 不作为当前余额来源。 |
| [异步通知](https://www.ejhcard.com/docs/zh/api/async-notification.html) | AUTH/AUTH_FAIL/AUTH_REVERSE；文档给出 sign 但未定义可验证签名算法。未开放不验签回调；当前采用流水同步。 |
| [交易明细](https://www.ejhcard.com/docs/zh/api/transaction-details.html) | card.trade.info 已接入；每段不超过 90 天，page 从 1、size=100；依据返回条数翻页，不把 totalSize 当全局总数。 |
| [提升额度](https://www.ejhcard.com/docs/zh/api/increase-limit.html) | card.change.info 已接入；cardAmount 是增加额度；新响应 orderNo/billAmount，不能期待旧 cardNo/CVV 返回。 |
| [卡余额](https://www.ejhcard.com/docs/zh/api/card-balance.html) | card.balance.info 已接入，读取 balance；不同币种独立汇总，不做未经依据的换汇。 |
| [卡列表旧页](https://www.ejhcard.com/docs/zh/api/card-list.html) / [新增页](https://www.ejhcard.com/docs/zh/api/query-card-list.html) | 同一 card.list.info；旧页用 pageSize，新页用 size，当前按新页 page/size/cardInfos。此次补全 UI 卡状态 1～8；无改动卡可用性规则。 |
| [冻结](https://www.ejhcard.com/docs/zh/api/lock-card.html) / [解冻](https://www.ejhcard.com/docs/zh/api/unlock-card.html) | 明确标记“待完善，暂时无法使用”；保持不提供可执行按钮。 |
| [状态码](https://www.ejhcard.com/docs/zh/appendix/status-codes.html) | 成功码 000000；现有常见错误有中文映射，其他保留响应码供查文档，不输出可能包含敏感信息的原始报文。并非全部错误码已有专用 UI 文案。 |
| [授权失败码](https://www.ejhcard.com/docs/zh/appendix/auth-decline-codes.html) | 为异步通知 respCode 说明；回调未安全接入前，不声称这些通知字段已展示。 |
| [币种](https://www.ejhcard.com/docs/zh/appendix/currencies.html) | 本次修正上述三个选择入口，并新增字典覆盖及保存回显测试。 |
| [代码加解密](https://www.ejhcard.com/docs/zh/appendix/encryption.html) | Java/C# 示例约定 DES/ECB/PKCS5（8 字节块）；已有客户端按供应商线协议实现，不作为本地存储加密方案。 |

## 保持边界与待确认项

- 不新增退款/撤销提交接口，不修改原有合并、移出流程。回款归属仍须核实，一卡金额/时间相近不足以证明账号归属；退款金额、币种可以不同于付款。
- 异步回调验签、各卡类型授权币种、部分订单日期格式仍需供应商确认；不为这些缺项伪造接口。
- 测试限本地模拟数据；不真实开卡、扣款、追加额度、销卡或触发退款。


## 回款时间显示

账号回款列表、账号弹窗和回款记录统一优先使用卡台流水 `createdAt`（记账时间），无有效记账时间时才回退到 `tradeTime`（交易发起时间）。EJH 的无时区时间按北京时间解释，明确带时区的时间保留其含义。多个有效、已确认到账流水显示最新记账时间；待核实、未到账流水不影响账号回款时间。不会用本机同步时间或人工确认时间替代。回款记录保留交易时间供对照，并按回款时间倒序；付款流水排序、金额计算、去重和自动归属逻辑不变。
