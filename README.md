# 精弘网络 2026 招新论坛后端

按照招新 Apifox 文档实现的 Go 后端，包含 10 个基础接口和 1 个 Agent 进阶接口。

## 已实现

- 用户注册、登录与 JWT HS256 鉴权
- 发布、分页/热门排序、详情、本人删除帖子
- 评论、点赞切换、批量点赞状态
- 管理员删除任意帖子
- MySQL 持久化和 Redis 点赞状态缓存（Redis 不可用时自动回退 MySQL）
- Eino + OpenAI 兼容模型、多轮消息记录、帖子草稿与显式二次确认
- Eino Tool 查询帖子和评论，模型请求后由后端执行并回填结果
- 全局错误响应、Recover、请求日志和 `logs/app.log`
- 可配置 CORS（默认允许开发环境跨域）

## 快速启动

1. 复制配置：

```powershell
Copy-Item .env.example .env
```

2. 至少修改 `.env` 中的 `JWT_SECRET` 和 `ADMIN_REGISTRATION_SECRET`。如需真实 Agent 模型，再填写 `LLM_API_KEY`、`LLM_BASE_URL` 和 `LLM_MODEL`。

3. 启动：

```powershell
docker compose up -d --build
```

4. 检查：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
docker compose ps
```

停止服务：

```powershell
docker compose down
```

默认不删除 MySQL volume。需要清空开发数据时才执行 `docker compose down -v`。

## 本机直接编译

```powershell
go mod tidy
go test ./...
go build -buildvcs=false -o forum-server.exe ./cmd/server
```

本机直跑时需将 `MYSQL_DSN` 和 `REDIS_ADDR` 改为本机可访问的地址。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/auth/register` | 注册 |
| POST | `/api/v1/auth/login` | 登录 |
| POST | `/api/v1/posts` | 发布帖子 |
| GET | `/api/v1/posts` | 列表，支持 `page`、`page_size`、`sort=latest|hot` |
| GET | `/api/v1/posts/{post_id}` | 帖子及评论详情 |
| DELETE | `/api/v1/posts/{post_id}` | 删除本人帖子 |
| POST | `/api/v1/posts/{post_id}/like` | 点赞/取消点赞 |
| POST | `/api/v1/posts/likes` | 批量点赞状态，最多 100 个 ID |
| POST | `/api/v1/posts/{post_id}/comment` | 评论 |
| DELETE | `/api/v1/admin/posts/{post_id}` | 管理员删除 |
| POST | `/api/v1/agent/chat` | Agent 多轮对话与草稿确认 |

除注册、登录和 `/healthz` 外均需携带：

```http
Authorization: Bearer <access_token>
```

完整字段以 [`../jinghong_backend_2026_docs/archive/INDEX.md`](../jinghong_backend_2026_docs/archive/INDEX.md) 中的本地 Apifox 快照为准。

创建 `admin` 账户时必须额外提供请求头：

```http
X-Admin-Registration-Secret: <ADMIN_REGISTRATION_SECRET>
```

## Agent 行为

- 未配置 LLM 时，接口仍提供本地查询回复和帖子草稿流程。
- 配置 LLM 后，帖子和评论查询通过 Eino Tool 执行，最多连续调用三轮。
- 请求起草帖子时只返回 `pending_action`，不会直接发布。
- 后续请求显式传入 `confirm_draft_id` 后才会创建帖子。
- 草稿绑定当前用户与 `session_id`，30 分钟后过期，且只能确认一次。

## 当前取舍

- 为快速交付使用 GORM `AutoMigrate`，没有引入单独迁移工具。
- 删除帖子采用事务硬删除，并清理对应评论和点赞。
- MySQL 是点赞权威数据源；Redis 只做缓存，故障不会阻断核心功能。
