# Pro 管理推送、额度监控与自动空间合并方案

> 当前状态：已实施。
>
> Pro Google/OpenAI 账号通过 Codex PKCE OAuth 手动授权，不依赖邮箱密码，也不调用外部项目服务。

## 0. Codex OAuth

“添加账号”生成并显示授权链接，用户复制到浏览器完成 Google/OpenAI 登录，再把 localhost 完整回调 URL 粘贴回系统。点击按钮不会自动打开外部页面。

OAuth 参数与 Sub2 OpenAI OAuth 保持一致：

```text
authorize: https://auth.openai.com/oauth/authorize
token: https://auth.openai.com/oauth/token
client_id: app_EMoamEEZ73f0CkXaXp7hrann
redirect_uri: http://localhost:1455/auth/callback
scope: openid profile email offline_access
PKCE: S256
id_token_add_organizations: true
codex_cli_simplified_flow: true
```

state 和 PKCE verifier 以 30 分钟有效的加密本地会话保存。授权码交换成功后立即删除会话，并加密保存 AT、RT、ID Token；RT 刷新支持 rotation，新 RT 存在时原子替换，不返回新 RT 时保留旧值。所有 OpenAI token 请求强制使用全局代理。

## 1. 页面结构

Pro 管理增加三个 Tab，样式与 Team 轮转保持一致：

1. Pro 账号
2. 推送设置
3. 合并空间选择

切换 Tab 时不离开 Pro 管理页面。

## 2. Pro 账号列表

增加平铺筛选：

- 全部
- 未空间合并
- 已空间合并

“已空间合并”的唯一判断标准是账号曾经成功执行过“合并个人空间”。即使账号之后已经移出 Team，仍然永久算作“已空间合并”。重新导入账号、重新推送或移回邮件管理都不会清除该历史标记。

列表增加以下字段和操作：

- 空间合并状态
- 推送线路：Sub2 / CPA
- 推送状态
- 5 小时额度
- 7 天额度
- 额度检测时间
- 四步流程状态：邀请空间、进入空间、合并空间、移出空间
- 最后错误
- 单账号推送
- 单账号查额度
- 立即执行或重试空间流程

顶部支持勾选后批量推送和批量查询额度。

## 3. 持久化状态

账号身份和加密 AT/RT 继续保存在邮件账号体系中，不复制凭证。

另外建立独立的 Pro 运行状态，记录：

```text
space_merged_once
space_merged_at
push_provider
push_status
pushed_at
downstream_account_id / auth_file_name
quota_5h
quota_7d
quota_checked_at
invite_status
accept_status
transfer_status
remove_status
target_admin_id
target_team_id
target_seat_type
last_error
```

`space_merged_once` 一旦变成 `true`，不会因为移出空间而恢复为 `false`。

## 4. Pro 推送设置

页面形式模仿 Team 轮转的推送设置：

- 左侧为 Sub2 设置
- 右侧为 CPA 设置
- 两条线路只能启用一条
- URL、密钥、分组、优先级和 WS 等参数按对应接口配置
- Pro 推送设置与 Team 轮转独立，切换 Pro 线路不会影响 Team 轮转

推送时只使用 Pro 管理中勾选且已经完成 Codex OAuth 的账号，不执行自动网页登录。

下游账号的套餐元数据使用：

```text
plan_type: pro
chatgpt_plan_type: pro
```

不写成 `self_serve_business_prolite`。CPA 的协议类型仍按接口要求保留 `codex`，Sub2 的凭证类型仍保留 `oauth`，但套餐身份明确为 `pro`。

AT 和 RT 必须完整才允许推送，ID Token 存在时也一并写入下游凭据。AT 临近到期时先通过 RT 自动刷新。

## 5. 额度监控

Pro 推送设置增加：

- 自动额度探测开关
- 检测间隔秒数，最小 10 秒
- 下一次检测倒计时
- 手动立即检测

只检测同时满足以下条件的账号：

```text
仍在 Pro 管理
已推送成功
推送线路与当前启用线路一致
尚未成功合并过空间
没有正在执行四步流程
```

额度全部通过当前启用的 Sub2 或 CPA 接口获取，不直接调用 OpenAI。

只有明确获得“7 天额度已使用 100%”或“7 天额度剩余 0%”时才触发空间流程。接口超时、401、缺少额度数据或解析失败时只记录检测失败，不触发空间操作。

## 6. 合并空间选择

配置固定的目标空间和执行参数：

- 母号
- Team 空间
- 邀请套餐
  - Standard 普通席位，默认值
  - Premium / 5x
- 自动执行开关
- 网络失败重试次数和间隔

额度耗尽后使用这里保存的空间和套餐，不临时随机选择母号。

所有 OpenAI 请求，包括席位检查、邀请、接受、合并和移出，必须强制使用项目全局代理。没有配置全局代理时停止执行并显示原因。

## 7. 额度耗尽后的四步流程

严格按以下顺序执行：

```text
邀请进入配置的 Team 空间
        ↓
账号接受邀请并进入空间
        ↓
合并个人空间
        ↓
从 Team 空间移出
```

“合并个人空间”成功时立即写入：

```text
space_merged_once = true
space_merged_at = 当前时间
```

即使第四步移出失败，该账号仍然显示“已空间合并”，但流程列必须明确显示“移出失败”。后台只重试移出，不会重新邀请或重新合并。

四步全部完成后停止该账号的额度监控，账号继续保留在 Pro 列表中供查询。

## 8. 失败恢复与幂等

每一步独立落库，服务重启后从未完成步骤继续：

- 邀请成功、进入失败：从进入空间继续
- 已进入、合并失败：从合并空间继续
- 已合并、移出失败：只重试移出
- 不会因为重启再次邀请
- 不会重复执行已经成功的空间合并

普通网络错误按设置重试。明确的业务错误保留状态，并允许在列表中手动点击“重试流程”。

## 9. 并发控制

额度检测采用批量并发，不逐账号串行等待。

四步空间流程采用以下控制：

- 账号级锁：同一个账号不能重复执行
- Team 级队列：同一个母号空间一次只运行一个四步流程
- 不同 Team 可以并行
- 移出步骤在同一母号下严格串行，避免 HTTP 429
- 上一轮未结束时，下一次定时检测不会重复启动该账号

这样可以避免手动操作、定时检测和服务重启造成重复邀请或重复合并。

## 10. 日志与测试

复用现有异步执行事件队列，记录 Pro 的额度检测、推送和四步流程，不阻塞业务请求。

实施时需要覆盖以下测试：

- 未合并和已合并筛选
- 移出后仍保持已合并
- Sub2 和 CPA 的 Pro 类型推送
- 5 小时和 7 天额度解析
- 7 天额度未耗尽时不触发
- 7 天额度耗尽时触发四步流程
- 额度接口异常时不误触发
- Standard 和 Premium / 5x 目标套餐
- 每一步失败后的断点恢复
- 服务重启后的任务恢复
- 同 Team 串行和不同 Team 并行
- 定时任务与手动操作同时触发
- 全局代理强制使用
- 下游推送失败、额度失败和移出失败
