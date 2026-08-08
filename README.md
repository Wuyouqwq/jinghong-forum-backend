# 精弘网络 2026 招新论坛后端

Go 论坛后端，实现招新文档中的 10 个基础接口和 Agent 进阶接口。

## 功能

- JWT 鉴权、用户与管理员权限校验
- 帖子发布、删除、评论、点赞及批量点赞状态
- 最新/热门排序、评论游标分页
- MySQL 持久化、Redis 缓存与故障回退
- Eino Agent 查询、帖子草稿及二次确认
- 限流、CORS、结构化日志、健康检查和版本化迁移

## 启动

```powershell
Copy-Item .env.example .env
docker compose up -d --build
```

开发环境至少修改 `.env` 中的 `JWT_SECRET`；启用真实 Agent 时再配置 `LLM_API_KEY`、`LLM_BASE_URL` 和 `LLM_MODEL`。

检查服务：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
Invoke-RestMethod http://127.0.0.1:8080/readyz
docker compose ps
```

停止服务：

```powershell
docker compose down
```

仅需清空开发数据时使用 `docker compose down -v`。

## 接口

接口字段、状态码和进阶行为见 [`docs/API.md`](docs/API.md)，完整定义以招新 Apifox 文档为准。

除注册、登录、`/healthz` 和 `/readyz` 外，请求均需携带：

```http
Authorization: Bearer <access_token>
```

生产环境注册管理员还需携带：

```http
X-Admin-Registration-Secret: <ADMIN_REGISTRATION_SECRET>
```

## Agent

- 未配置 LLM 时支持本地只读查询，草稿请求返回 `503`。
- 查询由 Eino Tool 执行；历史按用户和 `session_id` 隔离。
- 发布请求先生成 30 分钟有效的草稿，传入 `confirm_draft_id` 后才会创建帖子。
- 草稿绑定用户与会话，只能确认一次。

## 配置

可配置项及默认值见 [`.env.example`](.env.example)。时长使用 Go duration 格式，如 `24h`。

`APP_ENV=production` 时，服务拒绝以下配置：

- 默认 MySQL 凭据
- 默认或过短的 `JWT_SECRET`
- 少于 16 字节的 `ADMIN_REGISTRATION_SECRET`
- `CORS_ORIGINS=*`

反向代理地址通过 `TRUSTED_PROXIES` 配置。Redis 可使用 `REDIS_PASSWORD` 和 `REDIS_TLS` 加固。

热门排序参数默认参考社区实践：`gravity=1.2`、基础半衰期 `72h`、近期互动半衰期 `24h`、近期窗口 `168h`。应按实际内容周期调参。

## 本机开发与验证

```powershell
go mod download
go test -race -shuffle=on -count=1 ./...
go test -race -tags=integration -shuffle=on -count=1 ./internal/app
go vet ./...
go build -mod=readonly -buildvcs=false -o forum-server.exe ./cmd/server
```

集成测试需要可访问的 MySQL 和 Redis。本机运行时请相应设置 `MYSQL_DSN` 和 `REDIS_ADDR`。
