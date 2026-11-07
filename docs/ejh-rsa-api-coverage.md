# EJH 双重验签接口全量对照（2026-11-07）

已通读官方 `/docs/zh/rsa/` 中文文档全部 19 页，包括 11 个业务/通知页面、3 个附录、配置公钥、认证、公共参数、五种语言示例和更新日志。对照范围是 Space 当前实际使用的认证及 8 个业务 method；字段对照和模拟验签不代表已对商户进行真实开卡或销卡测试。

## 公共协议变化

- [获取认证](https://www.ejhcard.com/docs/zh/rsa/auth.html)：地址仍为 `POST https://www.ejhcard.com/api/v3/authenticate`。旧实现仅发送 `appKey`；新版新增秒级 `timestamp`、每次新生成的 16～32 位字母数字 `nonce`、认证完整 `url` 和 `sign`。
- 固定顺序签名原文：`appsecret={原AppSecret}&nonce={nonce}&timestamp={timestamp}&url={url}`。小写 `appsecret`，不 URL 编码，UTF-8。私钥 RSA PKCS#8，SHA256withRSA（PKCS#1 v1.5），标准 Base64。`appSecret` 不随认证请求发送。
- [公共参数](https://www.ejhcard.com/docs/zh/rsa/params-examples.html)：业务地址仍为 `POST https://www.ejhcard.com/api/v3/card`。保留原 `appKey`、DES/ECB/PKCS5 加密的 `param` 和原始 `Authorization: authToken`，新增只对加密后 Base64 `param` 字符串签名的 `sign`，不拼接其他字段。DES 密钥仍取 AppSecret 前 8 字节。
- 时间容差仍为 10 分钟、版本仍为 `3.0`、认证 `validity` 仍为毫秒。成功业务码仍须字符串 `000000`，不能将 HTTP 200 单独作为业务成功。
- [示例代码](https://www.ejhcard.com/docs/zh/rsa/samples.html)：Java/Python/Node.js/C#/Go 的原文拼接、PKCS#8、SHA256withRSA、Base64、DES 约定一致；Go 客户端已对齐，独立用公钥验签和解密校验八种业务请求。
- [更新日志](https://www.ejhcard.com/docs/zh/rsa/changelog.html)：2026-10-20 为不兼容的认证/业务签名升级；2026-10-21 新增开卡 `CARD` 回调。

## 当前调用逐项对照

| 用途 / 文档 | method 与请求 | 响应 / 对照结论 |
| --- | --- | --- |
| [开卡](https://www.ejhcard.com/docs/zh/rsa/api/create-card.html) | `card.open.info`；cardType/cardAmount/cardCurrency/billCurrency/useTime/activeDate/supportedMccGroup；原模板的 companyRate/authCount/extInfoyx 和可选订单字段 | 业务字段不变。orderNo/cardNo/validityDate/cvvPassword、费用及金额字段保留；activeDate 大于当天，MCC 四类保持。手动与补货共用签名客户端。 |
| [销卡](https://www.ejhcard.com/docs/zh/rsa/api/cancel-card.html) | `card.cancel.info`；orderNo | orderNo/status=SUCCESS 不变。保留现有受理结果检查、未知结果查询、自动销卡约束，不重发未知销卡。 |
| [卡片详情](https://www.ejhcard.com/docs/zh/rsa/api/card-query.html) | `card.query.info`；orderNo | cardNo/validityDate/cvvPassword/status/isLocked/actualBillAmount 等不变；手动刷新、开卡恢复与销卡复核均签名。 |
| [账户余额](https://www.ejhcard.com/docs/zh/rsa/api/balance-query.html) | `account.balance.info`；data={} | abroadList 下 currency/currencyName/balance 不变；连接测试及余额查询均签名。 |
| [卡余额](https://www.ejhcard.com/docs/zh/rsa/api/card-balance.html) | `card.balance.info`；orderNo | balance 不变。原币种和历史余额处理保留。 |
| [卡列表](https://www.ejhcard.com/docs/zh/rsa/api/query-card-list.html) | `card.list.info`；page 从 1、size 最大 100；可选 startTime/endTime/orderNo | 当前已使用 size/cardInfos。total 为本次列表数量，不据此提前终止全部分页。状态 1～8 与既有映射一致。 |
| [交易明细](https://www.ejhcard.com/docs/zh/rsa/api/transaction-details.html) | `card.trade.info`；tradeTime/endTime、page/size，可选 orderNo | 单次日期跨度不超过 90 天，size 最大 100；totalSize 为本次 tradeInfos 长度。既有分段、翻页、归属和去重匹配；新增签名。createdAt/退款类型及金额判定不变。 |
| [提额](https://www.ejhcard.com/docs/zh/rsa/api/increase-limit.html) | `card.change.info`；orderNo/cardAmount | cardAmount 是增加额度；返回 orderNo/billAmount。当前已按该结构处理，原未知结果不自动重放。 |
| [冻结](https://www.ejhcard.com/docs/zh/rsa/api/lock-card.html) | `card.lock.info` | 文档仍明确“待完善，暂时无法使用”；Space 不发送此请求。 |
| [解冻](https://www.ejhcard.com/docs/zh/rsa/api/unlock-card.html) | `card.unlock.info` | 同上，不擅自启用。 |
| [异步通知](https://www.ejhcard.com/docs/zh/rsa/api/async-notification.html) | 原 AUTH/AUTH_FAIL/AUTH_REVERSE，新增 CARD 开卡通知 | 文档有 sign 但没有完整的回调签名串和验签密钥规则。Space 继续主动查询，不新增无法验签的回调写入口；本轮请求双重验签不能代替回调验签。 |
| [状态码](https://www.ejhcard.com/docs/zh/rsa/appendix/status-codes.html) | 000000/000007/000008/000009/999999 与业务码 | 保留响应码及固定脱敏错误映射；000009 是通用失败，仅精确 token 错误才触发原有只读查询的单次重新认证。开卡/提额/销卡不因签名或网络失败自动重放。 |
| [授权失败码](https://www.ejhcard.com/docs/zh/rsa/appendix/auth-decline-codes.html) | 回调 respCode，如 10001～10020、88888、99999 | 与业务接口 code 不混用；未据该表把业务失败自动认定为认证失败。 |
| [币种](https://www.ejhcard.com/docs/zh/rsa/appendix/currencies.html) | 157 个币种 | 已与现有字典逐个比较，缺失 0、多余 0；无需修改币种或业务配置。 |

## 配置与密钥生命周期

[官方配置公钥说明](https://www.ejhcard.com/docs/zh/rsa/index.html)要求 RSA 至少 2048 位；Space 默认生成 2048 位，私钥 PKCS#8、公钥 X.509 SPKI，公钥复制为单行 Base64，正好对应官网“商户公钥 Base64 格式”。

1. Space → EJH 卡台 → 配置与模板 → 接口与更新，保留已保存 AppKey/AppSecret，点击“生成签名密钥”。私钥只以加密记录保存在当前实例数据库，不进入设置 JSON、普通接口响应或 Git。
2. 点击“复制公钥”，粘贴到 EJH 官网“配置商户公钥”。**2026-11-07 官网实际配置窗口明确：保存后重新生成 secretKey，旧 secretKey 立即失效。** 这一点未写在上述静态配置页中，部署操作须以该窗口为准。
3. 将 EJH 新生成的 secretKey 填入 Space 的 AppSecret，保存配置，然后点击“测试连接”。更换 AppSecret 会使客户端使用新的认证缓存范围，保留同一 RSA 密钥。
4. “重新生成密钥”需明确点击确认，会同时替换公私钥；再次执行第 2～3 步。普通打开/刷新页面和重复初次生成不旋转已有密钥。不会提供复制私钥按钮。

签名私钥只在服务端请求上下文加载，包括单卡明细的独立只读数据库快照。密钥与 AppKey/URL 绑定；已保存却无法解密、格式错误或商户不符时返回错误，不降级成无签名请求。尚未配置密钥的旧实例保留原调用兼容性，但不能据此声称能访问已停用的旧接口。

## 文档仍有歧义的部分

- 卡列表文字约定 validityDate=MMYY，示例写 2612；卡详情/开卡示例是 1226。保持 MMYY 校验，不据单个矛盾示例颠倒所有卡有效期。
- 可选 OrderActiveDate/OrderExpiryDate 的文字格式与示例不一致，本轮不改动原模板传递值。
- 实际认证通过需以商户配置公钥并更新新 AppSecret 后的只读连接测试为准；模拟请求测试不能证明商户权限、资金或真实卡操作已成功。
