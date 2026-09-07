# ChatGPT Space Merge

本机可视化的 ChatGPT Team/Business 空间合并工具。既可在任务台执行完整流程，也可按天拆分执行：

1. 母号邀请子号加入团队。
2. 子号接受邀请。
3. 子号将个人空间合并到团队空间。
4. 母号将子号移出团队。

顶部的“进入空间”“合并空间”“移出空间”菜单分别执行 1-2、3、4 步。三个分步菜单仍要求选择母号并上传当次可用的子号 AT；历史任务只用于显示账号阶段，不会复用旧 AT。

项目采用 Go 单进程后端并内嵌 Vue 3 + Vite 前端，默认只监听 `127.0.0.1:18120`。空间任务中的临时子号 Access Token 只在任务执行期间保存在进程内存中，不会写入数据库、历史或日志。

## 功能

- 母号与批量子号 JWT 解析、账号预览和团队 ID 自动提取。
- 多子号并发执行，每个账号独立显示四步状态、HTTP 状态与错误。
- 可中途停止；重复子号会在发请求前拦截。
- 邀请成功后若后续步骤失败，可自动尝试将子号移出团队。
- API 基址、TOS 版本、角色、并发、超时和三段等待时间均可视化配置；任务台和“进入空间”会在每次启动邀请任务时单独选择席位类型。
- 命名代理配置支持新增、编辑、删除、实际链路测试和执行代理下拉选择。
- 代理地址支持服务商线路格式 `host:port:username:password`，解析后自动转换为 `http://username:password@host:port`；密码中额外的冒号会保留。
- 母号支持命名保存、编辑、删除、只读有效性校验和任务下拉选择；AT 使用 AES-256-GCM 加密落盘且不会回传浏览器。
- 母号录入框可直接粘贴官网 Session JSON，浏览器只提取其中的 `accessToken`，并自动预览邮箱、计划、团队 ID 和到期时间；完整 Session 与 `sessionToken` 不会提交后端。
- 子号支持逐行粘贴、单个 JSON 文件导入，以及选择 CPA 导出的 JSON 文件夹批量导入；每个文件可包含一个或多个 `access_token/accessToken`。
- 设置、母号密文、最近 100 个任务和账号阶段保存在轻量 SQLite 数据库 `data/state.db`。
- 首次启动新版本时自动导入旧 `data/state.json`，成功后保留为 `data/state.json.migrated.bak`。
- 健康检查、Docker 配置、本地一键启动与停止脚本。
- 独立的 Team 轮转：持久化 Free 账号、邀请进入团队、绑定 Codex OAuth、推送 Sub2、监控 5 小时/7 天额度并按策略自动移出。

## 本地启动

要求 Go 1.24 或更高版本。

双击 `start-local.cmd`，或者在 PowerShell 中执行：

```powershell
go build -o chatgpt-space-merge.exe ./cmd/server
./chatgpt-space-merge.exe
```

打开 `http://127.0.0.1:18120/`。

首次打开会进入登录页，默认账号为 `admin`、密码为 `admin`。登录后可点击右上角钥匙按钮修改密码（需要输入当前密码和两次新密码）。

## turb 注册结果回调

`turb-gpt-free-register` 的 Roxy 注册在最终读取 `/api/auth/session` 并拿到 `accessToken` 后才返回成功。因此可以让 turb 专门负责注册，完成后自动把 AT 推送到本项目的“Team 轮转”账号列表，不需要再次在本项目登录邮箱获取 AT。

空间合并项目提供接口：

```text
POST /api/integrations/turb/register
Authorization: Basic <Space Console 账号密码>
Content-Type: application/json
```

请求体至少包含 `access_token`，也可带 `email`、`name`、`user_id`、`account_id`、`plan_type`。AT 会在服务端加密后写入 Free 账号库。

如果 turb 与本项目不在同一台机器，需让本项目监听可访问的地址，例如设置 `APP_ADDR=0.0.0.0:18120` 并在防火墙/反向代理中限制来源；turb 的 `REMOTE_IMPORT_URL` 填实际地址。修改本项目登录密码后，记得同步更新 turb 的 `REMOTE_IMPORT_PASSWORD`。

停止后台启动的服务可双击 `stop-local.cmd`。

## Docker

```bash
docker compose up -d --build
docker compose logs -f --tail=100
```

镜像会自动安装 `python3`、`py3-pip`、`libstdc++` 和 `curl_cffi`，并复制纯协议登录/OAuth 脚本；正式环境不需要在宿主机单独安装 Python。依赖或 Dockerfile 变化后请重新构建镜像，不要只重启旧容器：

```bash
docker compose build --no-cache
docker compose up -d
docker compose exec chatgpt-space-merge python3 -c "import curl_cffi; print('curl_cffi ok')"
```

Compose 仍只将端口发布到宿主机回环地址。

## 配置

环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `APP_ADDR` | `127.0.0.1:18120` | HTTP 监听地址 |
| `APP_DATA_DIR` | `./data` | 配置与脱敏历史目录 |
| `APP_MASTER_KEY` | 自动生成 | 可选，Base64 编码的 32 字节母号凭据主密钥 |

页面中的默认上游基址为 `https://chatgpt.com/backend-api`。代理支持 `http://`、`https://`、`socks5://` 和 `socks5h://`。代理测试由后端经待测代理请求当前上游基址；收到非 `407` 的 HTTP 响应即表示代理链路可达，并显示状态码和耗时。测试请求不携带 Access Token。

未设置 `APP_MASTER_KEY` 时，应用首次启动会在数据目录生成 `data/.master-key`。停止服务后，应将该文件与 `state.db` 一起备份；丢失或更换密钥后，已保存的母号 AT 将无法解密。生产部署建议通过环境变量提供并在服务器外安全备份固定主密钥。

## 任务模式

| 菜单 | 执行步骤 | 当次需要的凭据 |
| --- | --- | --- |
| 任务台 | 邀请、接受、合并、移出 | 母号配置 + 子号 AT + 本次邀请席位 |
| 进入空间 | 邀请、接受 | 母号配置 + 子号 AT + 本次邀请席位 |
| 合并空间 | 合并 | 母号配置 + 子号 AT |
| 移出空间 | 移出 | 母号配置 + 子号 AT |

数据库会按团队 ID 与子号 User ID 累计已完成阶段，并在分步菜单的凭据预览中提示“已进入 / 已合并 / 已移出”。该提示不会替代实时凭据校验，也不会自动跳过操作。

## Team 轮转

“Team 轮转”与空间合并任务完全独立，账号会持久记录邀请、进入、OAuth、推送、额度和移出六个阶段。第一版流程为：

1. 批量导入 Free 账号 AT。
2. 为账号选择母号和当次邀请席位，完成邀请与接受。
3. 手动绑定 Codex OAuth JSON，后续版本再接入自动授权逻辑。
4. 将 OAuth 账号推送到已配置的 Sub2 OpenAI 分组。
5. 查询 Codex 5 小时与 7 天窗口的剩余额度。
6. 按账号选择“5 小时耗尽”或“7 天耗尽”，后台每 2 分钟检查并自动移出团队。

六个阶段均可在账号表格中手动标记为成功、失败或待处理；已手动完成邀请后，再执行入队只会接受邀请，不会重复发送。Sub2 推送支持同时选择多个 OpenAI 分组，并可通过模型清单写入账号的 `credentials.model_mapping`。

Free 源 AT、OAuth AT/RT 和 Sub2 管理员密码均使用 AES-256-GCM 加密保存。自动移出只调用团队成员移出接口，不会删除 Sub2 中已推送的账号。接口设置中选定全局代理后，ChatGPT Backend API、Codex 额度接口和 OpenAI OAuth 刷新请求都会强制使用该代理。

## 接口流程

| 步骤 | 方法与路径 | 凭据 |
| --- | --- | --- |
| 邀请 | `POST /accounts/{team_id}/invites` | 母号 AT |
| 接受 | `POST /accounts/{team_id}/invites/accept` | 子号 AT |
| 合并 | `POST /accounts/transfer` | 子号 AT |
| 移出 | `DELETE /accounts/{team_id}/users/{user_id}` | 母号 AT |

成功状态按所有 `2xx` 处理，包括空响应的 `204`。

## 安全与兼容性

- ChatGPT Backend API 是官网私有接口，字段或流程可能随官网更新。当前默认请求体严格按提供的脚本实现，其中接受邀请和空间合并字段在原脚本中标注为推测值。
- 本项目现在提供管理员登录层，默认回环监听仍是推荐的安全边界。若改成 `0.0.0.0` 供 turb 远程回调，必须在防火墙/反向代理中限制来源并启用 HTTPS；回调接口另需 HTTP Basic Auth。
- 合并个人空间可能迁移或改变账号数据归属。执行前应确认账号、团队和数据备份情况，并确保操作符合服务条款及适用规则。
- SQLite 数据库可能保存代理地址及其中的代理凭据，`data/` 已被 Git 忽略。
- 母号 AT/RT 仅以 AES-GCM 密文写入 `state.db`；API 列表、任务历史和普通日志均不返回或记录明文。
- OpenAI 账号管理支持粘贴或选择 Session JSON/JSON 文件夹批量提取 AT，自动识别账号名称、邮箱和计划后加密保存；可同时保存 RT，用于单个/批量刷新 AT；支持单个/批量 AT 有效性检测，以及按 Sub2API 兼容的 `/wham/usage` 与 `rate-limit-reset-credits` 接口查询剩余重置次数，查询结果仅保存脱敏数量和时间。
- Team 轮转的源 AT、Codex OAuth AT/RT 与 Sub2 管理员密码均加密保存；列表接口只返回是否已保存，不返回凭据明文。

## 验证

```powershell
go test ./...
go vet ./...
go build ./cmd/server
cd frontend
npm run check
npm run build
```
