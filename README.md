# ChatGPT Space Merge

本机可视化的 ChatGPT Team/Business 空间合并工具。流程来自提供的脚本：

1. 母号邀请子号加入团队。
2. 子号接受邀请。
3. 子号将个人空间合并到团队空间。
4. 母号将子号移出团队。

项目采用 Go 单进程后端并内嵌原生前端，默认只监听 `127.0.0.1:18120`。Access Token 只在任务执行期间保存在进程内存中，不会写入配置、历史或日志。

## 功能

- 母号与批量子号 JWT 解析、账号预览和团队 ID 自动提取。
- 多子号并发执行，每个账号独立显示四步状态、HTTP 状态与错误。
- 可中途停止；重复子号会在发请求前拦截。
- 邀请成功后若后续步骤失败，可自动尝试将子号移出团队。
- API 基址、TOS 版本、角色、席位类型、并发、超时和三段等待时间均可视化配置。
- 命名代理配置支持新增、编辑、删除、实际链路测试和执行代理下拉选择。
- 代理地址支持服务商线路格式 `host:port:username:password`，解析后自动转换为 `http://username:password@host:port`；密码中额外的冒号会保留。
- 母号支持命名保存、编辑、删除、只读有效性校验和任务下拉选择；AT 使用 AES-256-GCM 加密落盘且不会回传浏览器。
- 母号录入框可直接粘贴官网 Session JSON，浏览器只提取其中的 `accessToken`，并自动预览邮箱、计划、团队 ID 和到期时间；完整 Session 与 `sessionToken` 不会提交后端。
- 最近 100 个任务的脱敏历史保存在 `data/state.json`。
- 健康检查、Docker 配置、本地一键启动与停止脚本。

## 本地启动

要求 Go 1.24 或更高版本。

双击 `start-local.cmd`，或者在 PowerShell 中执行：

```powershell
go build -o chatgpt-space-merge.exe ./cmd/server
./chatgpt-space-merge.exe
```

打开 `http://127.0.0.1:18120/`。

停止后台启动的服务可双击 `stop-local.cmd`。

## Docker

```bash
docker compose up -d --build
docker compose logs -f --tail=100
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

未设置 `APP_MASTER_KEY` 时，应用首次启动会在数据目录生成 `data/.master-key`。该文件与 `state.json` 必须一起备份；丢失或更换密钥后，已保存的母号 AT 将无法解密。生产部署建议通过环境变量提供并在服务器外安全备份固定主密钥。

## 接口流程

| 步骤 | 方法与路径 | 凭据 |
| --- | --- | --- |
| 邀请 | `POST /accounts/{team_id}/invites` | 母号 AT |
| 接受 | `POST /accounts/{team_id}/invites/accept` | 子号 AT |
| 合并 | `PATCH /accounts/{team_id}` | 子号 AT |
| 移出 | `DELETE /accounts/{team_id}/users/{user_id}` | 母号 AT |

成功状态按所有 `2xx` 处理，包括空响应的 `204`。

## 安全与兼容性

- ChatGPT Backend API 是官网私有接口，字段或流程可能随官网更新。当前默认请求体严格按提供的脚本实现，其中接受邀请和空间合并字段在原脚本中标注为推测值。
- 本项目没有额外登录层，默认回环监听是安全边界。不要直接改成 `0.0.0.0` 暴露到公网；经反向代理使用时必须增加 HTTPS 和身份认证。
- 合并个人空间可能迁移或改变账号数据归属。执行前应确认账号、团队和数据备份情况，并确保操作符合服务条款及适用规则。
- 配置文件可能保存代理地址及其中的代理凭据，`data/` 已被 Git 忽略。
- 母号 AT 仅以 AES-GCM 密文出现在 `state.json`；API 列表、任务历史和普通日志均不返回或记录明文。

## 验证

```powershell
go test ./...
go vet ./...
go build ./cmd/server
node --check webui/static/assets/app.js
```
