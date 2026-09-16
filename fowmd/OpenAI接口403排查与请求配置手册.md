# OpenAI 接口请求配置与 403 风控排查手册

更新日期：2026-09-16

适用项目：chatgpt-space-merge、chapt-space-user

验证范围：邮件管理手动刷新 GPT 信息，复用现有 Pro 套餐识别请求头。

## 1. 先看结论

本手册用于减少请求配置不一致造成的失败，并帮助定位 403，不保证任何配置永久不被拦截，也不用于绕过访问权限、地域限制或验证码。

当前已验证可用的组合是：

- 使用已有 AT，只查询信息，不刷新 RT、不自动重登。
- 使用全局代理；没有全局代理就停止，不回退直连。
- 使用 curl_cffi 的 Chrome136 传输指纹，配套 Chrome136 User-Agent。
- 复用 Pro 套餐识别的完整请求头，正确设置 Origin、Referer、设备 ID 和目标路由。
- 同一服务内，GPT 信息刷新最多并发 4 个账号，账号启动间隔约 400～700ms。
- 分别记录套餐接口和账号信息接口的结果，依据状态码、响应头和响应内容分类处理。
- 有限重试；401、普通 403 不盲目重试，遇到冷却要求遵守 Retry-After。

这里使用的是 ChatGPT 网页内部接口，不是公开的 OpenAI Platform API。内部接口可能变化，请以本项目源码和最新实际响应为准，不能将本手册的自定义请求头当成官方公开 API 的通用要求。

**不是整个项目所有请求都采用 Chrome136。** 本文描述的是新 GPT 信息刷新路径和其复用的 Pro 请求头。OAuth、邀请、确认、移出等流程须按各自实现检查，不应为了本功能统一改动已经验证的登录协议。

## 2. 本次问题的证据和结论边界

本次小范围比较中，精简请求有过成功，也出现了 403；完整 Pro 风格请求头配合 Chrome136 查询成功。失败样本包含：

~~~text
HTTP 403
Content-Type: text/html; charset=UTF-8
cf-mitigated: challenge
~~~

该样本正文没有旧识别代码依赖的 cf-、cloudflare、just a moment 等关键词。旧代码只匹配正文，漏掉了响应头明确给出的 challenge，因此当成普通 403，不执行对应的有限重试。

现在保存 Content-Type、CF-Mitigated、CF-Ray，并优先利用 cf-mitigated: challenge 识别此类响应。本次真实刷新中，两个接口均首次 HTTP 200，套餐和创建时间已保存，AT/RT 未变。

可以确认的是“出现过 Cloudflare challenge，旧检测遗漏响应头”。不能仅凭这次比较断言：

- Chrome131 一定失败，Chrome136 一定成功。
- 某一个请求头就是唯一根因。
- 一定是某个代理出口信誉差，或已经证明出口发生变化。
- 普通 403 就是死号，或套餐接口成功就代表 /me 也必然成功。

server: cloudflare 本身不是拦截证据，正常响应也可能出现。CF-Ray 的节点后缀也不能用于证明代理出口 IP 改变。

## 3. 查询接口与数据来源

| 用途 | 方法 | 完整地址 |
| --- | --- | --- |
| 套餐识别 | GET | https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-480 |
| GPT 账号信息 | GET | https://chatgpt.com/backend-api/me |

两个请求均无请求体，使用该邮件账号当前保存的 access_token。

- timezone_offset_min=-480 对应北京时间的浏览器时区偏移表达。
- 套餐优先取套餐接口 entitlement；缺失时再考虑 /me 返回的套餐，最后参考 AT 的 JWT claim。
- JWT 是令牌签发时的信息，不证明当前实时套餐。只有 JWT 兜底，不会被当成远端刷新全部成功。
- 多空间响应优先匹配 AT 中的 AccountID，其次 default，只有唯一条目时才采用该条目；无法确定不任意挑其他空间。
- 创建时间只取 /me 顶层 created，单位是 Unix 秒，不是毫秒。
- 创建时间不是导入时间、进入 Team 时间或当前订阅开始时间。数据库保存 UTC，页面转换为北京时间显示。
- 无有效 created 时保留历史值，不用本地时间伪造创建时间。

## 4. 当前显式请求头

下面是共享构造器设置的完整显式请求头。尖括号中的内容是占位符，不能原样发送。

~~~http
Accept: */*
Accept-Language: zh-CN,zh;q=0.9,en;q=0.8
Authorization: Bearer <saved_access_token>
Origin: https://chatgpt.com
Referer: https://chatgpt.com/
oai-device-id: <stable_per_account_device_id>
oai-language: zh-CN
Sec-Fetch-Dest: empty
Sec-Fetch-Mode: cors
Sec-Fetch-Site: same-origin
User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36
~~~

此外，必须按具体接口构造以下两个项目现用的目标头：

| 接口 | x-openai-target-path | x-openai-target-route |
| --- | --- | --- |
| 套餐查询 | /backend-api/accounts/check/v4-2023-04-27 | /backend-api/accounts/check/v4-2023-04-27 |
| 账号信息 | /backend-api/me | /backend-api/me |

注意事项：

1. 目标头不带 timezone_offset_min 查询参数，不能把套餐路由复制到 /me。
2. 当前两个 GET 不显式设置 Content-Type，不应随意添加 JSON 请求体。
3. curl_cffi 还会根据 impersonate 配置生成浏览器默认头；上表不是抓包中所有最终头的穷举。
4. 不手工混用其他 Chrome 版本的 client hints。只改 User-Agent 不等于更换 TLS/HTTP 传输指纹。
5. 当前路径不需要从别的账号复制 Cookie、CSRF、Sentinel 或验证码令牌，不应随意拼接。
6. 不要把这套 GET 头原样套到邀请、同意、退出、OAuth。那些接口的方法、请求体、账号上下文和代理归属各不相同。

共享实现见 [plan.go](../internal/workflow/plan.go) 中的 newAccountPlanRequest。

### 设备 ID

当前复用 stablePlanDeviceID：

1. 种子优先取 AT 解出的邮箱，再取 AccountID，最后才取 token 前 32 字符。
2. 对 chatgpt-plan: 加去空格、小写后的种子计算 SHA256。
3. 取前 16 字节，设置 UUID 版本和 variant 位，格式化为 UUID。
4. 相同种子得到稳定 ID，两路查询和重试保持相同。

这是项目生成的确定性设备 ID，不是从真实浏览器提取的设备标识；设备 ID 也不是授权凭据。不得记录 token 兜底种子的原文。

## 5. 传输层、代理和超时

GPT 信息请求的核心传输配置如下，变量由现有配置和请求构造器传入：

~~~python
from curl_cffi import CurlOpt, requests

with requests.Session(
    impersonate="chrome136",
    curl_options={CurlOpt.NOPROXY: ""},
) as session:
    session.proxies = {"http": proxy_url, "https": proxy_url}
    response = session.get(
        url,
        headers=headers,
        timeout=20,
        allow_redirects=False,
    )
~~~

实际实现见 [gpt_info.go](../internal/workflow/gpt_info.go) 内嵌的 gptInfoBrowserScript。

- TLS 证书验证保持开启，不用 verify=False 解决 403。
- NOPROXY 置空，防止环境 NO_PROXY 绕过显式配置的全局代理。
- 不自动跟随重定向，保留原始状态，避免将登录页误认为账号 JSON。
- 单次网络超时 20 秒；Go 子进程上限 25 秒；单账号任务上下文上限 3 分钟。
- 两个接口都读取全局代理配置，不采用母号专属代理。
- 母号操作 OpenAI 的既有专属代理规则不受此功能影响；OAuth 整段会话的既有代理绑定规则也不变。
- 正式环境要检查容器内代理地址可达性。容器中的 127.0.0.1 指容器自己，不是宿主机。
- 代理协议、端口、认证错误或出口限制，不能靠堆请求头修复。

**同一个代理 URL 不等于同一个出口 IP。** 此功能每个接口、每次尝试各自创建 Python/curl 会话，不共享 Cookie 或 TCP 会话。若代理服务动态轮换出口，两个请求可能使用不同出口。本功能没有显式换代理或强制换 IP 的逻辑，不要把重试描述成“必定更换 IP”。

## 6. 并发、节奏与 100 个账号的行为

| 项目 | 当前行为 |
| --- | --- |
| 单个服务内 GPT 信息刷新并发 | 最多 4 个账号，多个批次共享限制 |
| 每个账号 | 套餐和 /me 两个 GET 同时查询 |
| 本功能同时在途接口请求 | 最多 8 个，不是 4 个 |
| 账号启动节奏 | 复用 Pro 的启动节流器，计划间隔 400～700ms |
| 重复点击同一账号 | 复用正在运行的任务，避免跨批次重复查询 |
| 本地任务进度轮询 | 前端约每 1.5 秒查询本系统，不额外请求 OpenAI |
| 缺 AT、已删除等账号 | 跳过远端查询，返回对应结果 |

例如选中 100 个有效账号：

1. 任务建立后立即返回，后台开始执行，不一次性发送 200 个请求。
2. 最多保持 4 个账号同时处理，每个账号两路并发。
3. 一个账号结束释放名额，下一个继续，不要求一组 4 个全部结束才启动下一组。
4. 两个接口全部首次成功时，共发送 200 次 OpenAI GET；失败重试会增加次数。
5. 失败只重试对应接口，成功的另一接口不重复查询。

400～700ms 是账号的计划启动间隔，不是每个 HTTP 请求、两路查询之间或每次重试的统一间隔。慢请求、排队、调度延迟可能让实际间隔更长，也不能据此保证 100 个账号在固定时间内完成。

该限制只覆盖单个服务实例的 GPT 信息任务。Pro 自身工作池、Team、OAuth 等并不共用这一账号并发上限；多个容器也没有分布式总限流。Pro 与 GPT 信息刷新共享启动节奏，不代表共享同一个工作池。

## 7. 错误分类与当前有限重试

以下是新增 GPT 信息刷新的策略，不代表所有旧接口都采用相同策略。

| 响应或错误 | 当前处理 |
| --- | --- |
| HTTP 200 且合法账号 JSON | 解析对应字段 |
| HTTP 200 但 HTML、无效 JSON 或错误对象 | 失败，不当作有效账号数据 |
| HTTP 401 | 不重试，提示 AT 过期或失效，不自动刷新 RT |
| HTTP 403 且 cf-mitigated 为 challenge | 识别 CF challenge，有限重试 |
| HTTP 403 且正文命中既有 CF 特征 | 作为 CF 线索，有限重试 |
| 其他 HTTP 403 | 不重试，不判死号 |
| 网络错误、状态 0、408、429、5xx | 有限重试 |
| 其他错误、重定向 | 记录失败，不无限循环 |

每个接口最多 3 次总尝试，也就是最多额外重试 2 次。没有 Retry-After 时，重试等待通常为 1 秒、2 秒。

Retry-After 的处理：

- 支持正整数秒数和 HTTP 日期。
- 等待不超过 30 秒时按冷却要求处理；HTTP 日期不会缩短原有基础退避。
- 冷却超过 30 秒则结束当前接口任务，提示冷却后再手动刷新，不持续占住工作线程。
- 到达最大次数即停止；没有在后台偷偷无限重试或冷却后自动续跑。

通用运维原则：429 不一定只是瞬时流量过大，也可能是额度等限制；权限、账单和明确访问限制不能依靠反复重试解决。持续 challenge 应停止批量重试并排查，必要时使用正常交互登录或服务方支持渠道。

**Pro 原套餐识别仍有自己的重试策略：最多 2 次尝试，不自动重试普通/CF 403。** 共享请求头并不意味着两个入口的重试行为也完全相同。

## 8. 数据保护与部分成功

- 只使用已存 AT，不通过 RT 续期，避免影响其他系统复用 RT。
- 两个接口独立保留结果，一个成功、另一个失败时记为部分成功。
- 套餐成功但 /me 被拦截，可以更新套餐，创建时间保留旧值。
- /me 成功而套餐失败时，仅使用确实返回的可用字段，不虚构套餐。
- 两路均失败不清空原有有效套餐或创建时间。
- AT 在查询期间改变，或者账号已删除、重建时，旧查询结果不覆盖新账号状态。
- GPT 信息刷新失败不会直接标记死号、移出空间或执行重登。
- 不将“这次请求失败”混同于“历史缓存数据不存在”；排查时同时看 checked_at。

## 9. 以后发生 403，按这个顺序排查

1. **确定具体入口和接口。** 是邮件刷新信息、Pro 套餐查询，还是 OAuth？是套餐 GET 还是 /me？不要只看页面上一句“刷新失败”。
2. **确认部署版本。** 正式环境是否已重新构建镜像，实际运行的是否仍是旧二进制？仅重启旧容器不会加载新代码。
3. **看每路最终状态。** 分开检查 HTTP 状态、attempts、content_type、cf_mitigated、cf_ray 和时间。
4. **区分认证与 challenge。** 401 先处理 AT；403 HTML 且 challenge 优先排查请求环境；403 JSON 则查看明确业务错误，不能一律归因 CF。
5. **核对传输和头。** curl_cffi 是否支持 chrome136，UA 是否一致，Origin/Referer/目标路径是否与当前接口一致。
6. **核对实际网络环境。** 从运行服务的容器检查代理可达性、协议、DNS、证书和出口服务状态，不以宿主机浏览器能打开作为充分证据。
7. **核对并发来源。** 是否同时开了多个批次、Pro 查询、其他任务或多个容器？本功能限流不等于整个出口的总限流。
8. **小范围验证。** 待冷却后选一个已授权、AT 有效的账号手动刷新，记录两路结果。不要持续拿 100 个账号反复试错。
9. **保留脱敏证据。** 按下一节记录环境和响应信息，再决定是否需要调整代码。

403 不自动意味着死号，也不自动意味着指纹错误。必须结合认证状态、响应类型和环境证据判断。

## 10. 日志能看到什么，还需要记录什么

当前已持久化的每路诊断字段：

~~~text
ok
http_status
attempts
error
content_type
cf_mitigated
cf_ray
~~~

汇总字段包括 status、checked_at、plan_source，以及 plan/me 两路结果。

这些是**最终尝试的状态**，不是每次请求的完整报文历史。前一次 CF-Ray 可能被后一次成功响应替换，完整响应正文没有持久化。不能声称目前数据库已保存所有排查证据。

提交问题时建议补充以下脱敏信息；这些是建议收集项，不代表目前都已经自动落库：

~~~text
时间：UTC 或注明时区的准确时间
项目及部署版本：提交号、镜像标识
功能入口：邮件 GPT 信息刷新 / Pro / 其他
接口：GET + 路径，不含敏感参数
账号：内部 ID 或脱敏邮箱
代理：配置名称，不含用户名、密码、完整认证 URL
运行环境：宿主机 / 容器，Python 和 curl_cffi 版本
执行规模：账号数量、并发任务、是否多实例
套餐接口：状态码、次数、Content-Type、CF-Mitigated、CF-Ray
/me 接口：状态码、次数、Content-Type、CF-Mitigated、CF-Ray
错误摘要：脱敏后的错误码或有限长度正文摘要
~~~

禁止在手册、Git、聊天、公开 issue 中粘贴完整 Authorization、AT、RT、Cookie、密码、TOTP 密钥和带认证信息的代理 URL。HAR 和完整请求头导出前必须脱敏。

## 11. Docker 部署检查

当前两个 Dockerfile 都已经包含 Python 3.11、curl_cffi、pyotp、CA 证书、时区数据及既有 OAuth 所需的 Node/runtime。GPT 信息请求脚本内嵌在 Go 二进制中，不需要再单独上传脚本。

部署本次实现时，需要重新构建并启动镜像，不是只 restart：

~~~sh
# 在 chatgpt-space-merge 项目目录
docker compose up -d --build chatgpt-space-merge

# 在 chapt-space-user 项目目录
docker compose up -d --build chapt-space-user
~~~

检查运行镜像中的依赖，例如：

~~~sh
docker compose exec chatgpt-space-merge python3 -c "import sys, curl_cffi; print(sys.version); print(curl_cffi.__version__)"
~~~

另一个项目替换服务名为 chapt-space-user。以上依赖检查不请求 OpenAI。

目前 Dockerfile 未锁定 curl_cffi 的具体版本。因此“依赖已经安装”不等于所有时间重新构建都得到完全相同版本；故障排查应记录实际版本。本文只记录现状，没有另行改动依赖版本。

TZ=Asia/Shanghai 只影响本地显示等时区行为，不会让无效 AT 生效，也不会解决网络 403。

## 12. 本地模拟验证

在每个项目根目录执行，下面测试使用本地模拟数据，不使用真实账号：

~~~powershell
go test ./internal/workflow -run 'TestRefreshGPTInfo|TestGPTInfo' -count=1
go test ./internal/httpapi -run 'TestMailGPTInfo' -count=1
~~~

curl_cffi 本地传输集成测试需要显式开启：

~~~powershell
$env:GPT_INFO_CURL_TEST = '1'
try {
    go test ./internal/workflow -run TestGPTInfoCurlTransport -count=1
} finally {
    Remove-Item Env:GPT_INFO_CURL_TEST -ErrorAction SilentlyContinue
}
~~~

现有覆盖包括完整请求头、Chrome136、强制代理且不被 NO_PROXY 绕开、仅响应头提示 CF challenge 时的重试，以及跨批次 4 账号并发、启动节奏和去重。模拟通过只能证明本地行为符合预期，不能保证远端始终接受请求。

本手册整理前，两个项目的 Go 测试、静态检查和构建已通过；真实验证仅能证明当时被测试账号和环境成功，不代表所有账号、代理和未来接口状态。

## 13. 源码索引与官方资料边界

| 内容 | 位置 |
| --- | --- |
| Pro/GPT 共享请求头、设备 ID | [internal/workflow/plan.go](../internal/workflow/plan.go) |
| Chrome136 请求、双接口、超时和重试 | [internal/workflow/gpt_info.go](../internal/workflow/gpt_info.go) |
| GPT 信息批量任务、并发和去重 | [internal/httpapi/mail_gpt_info.go](../internal/httpapi/mail_gpt_info.go) |
| 400～700ms 共享启动节流 | [internal/httpapi/pro_accounts.go](../internal/httpapi/pro_accounts.go) |
| 诊断数据结构 | [internal/model/gpt_info.go](../internal/model/gpt_info.go) |
| 持久化与旧结果保护 | [internal/store/gpt_info.go](../internal/store/gpt_info.go) |
| 协议和传输模拟测试 | [internal/workflow/gpt_info_test.go](../internal/workflow/gpt_info_test.go) |
| 任务并发和隔离测试 | [internal/httpapi/mail_gpt_info_test.go](../internal/httpapi/mail_gpt_info_test.go) |
| 部署依赖 | [Dockerfile](../Dockerfile)、[docker-compose.yml](../docker-compose.yml) |

已核对 OpenAI Docs 的[公开 API 错误码说明](https://developers.openai.com/api/docs/guides/error-codes)：认证、地区限制、速率和额度限制需要区分处理，冷却和有限退避不能代替修复权限或账单问题。

该官方页面针对公开 API，**不是**本文 ChatGPT 网页内部接口、设备 ID、目标头或 Chrome136 配置的官方规范。本文这些实现细节来自本项目源码和本次实测。
