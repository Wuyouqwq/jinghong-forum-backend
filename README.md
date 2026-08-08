# 精弘网络 2026 招新论坛后端

按照招新 Apifox 文档实现的 Go 后端，包含 10 个基础接口和 1 个 Agent 进阶接口。

## 已实现

- 用户注册、登录与 JWT HS256 鉴权
- 发布、分页/热门排序、评论游标分页详情、本人删除帖子
- 评论、点赞切换、批量点赞状态
- 管理员删除任意帖子
- MySQL 持久化和 Redis 点赞状态缓存（Redis 不可用时自动回退 MySQL）
- Eino + OpenAI 兼容模型、多轮消息记录、帖子草稿与显式二次确认
- Eino Tool 查询帖子和评论，模型请求后由后端执行并回填结果
- 全局错误响应、Recover、请求日志和 `logs/app.log`
- 可配置 CORS（默认允许开发环境跨域）
- Redis/本机双层限流、JWT 用户与角色实时校验
- 版本化数据库迁移、外键级联、互动计数和查询索引
- 日志轮转、就绪检查、Agent 历史预算与自动清理
- 人性化热门排序：独立外部互动、对数压缩、基础/近期双层时间衰减与旧帖复活

## 快速启动

1. 复制配置：

```powershell
Copy-Item .env.example .env
```

2. 开发环境至少修改 `.env` 中的 `JWT_SECRET`。如需真实 Agent 模型，再填写 `LLM_API_KEY`、`LLM_BASE_URL` 和 `LLM_MODEL`。

3. 启动：

```powershell
docker compose up -d --build
```

4. 检查：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
Invoke-RestMethod http://127.0.0.1:8080/readyz
docker compose ps
```

停止服务：

```powershell
docker compose down
```

默认不删除 MySQL volume。需要清空开发数据时才执行 `docker compose down -v`。

## 本机直接编译

```powershell
go mod download
go test -count=1 ./...
go vet ./...
go build -mod=readonly -buildvcs=false -o forum-server.exe ./cmd/server
```

本机直跑时需将 `MYSQL_DSN` 和 `REDIS_ADDR` 改为本机可访问的地址。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/auth/register` | 注册 |
| POST | `/api/v1/auth/login` | 登录 |
| POST | `/api/v1/posts` | 发布帖子 |
| GET | `/api/v1/posts` | 列表，支持 `page`、`page_size`、`sort=latest|hot` |
| GET | `/api/v1/posts/{post_id}` | 帖子及评论详情；`comments_limit=1..100`、`after_comment_id` 游标分页 |
| DELETE | `/api/v1/posts/{post_id}` | 删除本人帖子 |
| POST | `/api/v1/posts/{post_id}/like` | 点赞/取消点赞 |
| POST | `/api/v1/posts/likes` | 批量点赞状态，最多 100 个 ID |
| POST | `/api/v1/posts/{post_id}/comment` | 评论 |
| DELETE | `/api/v1/admin/posts/{post_id}` | 管理员删除 |
| POST | `/api/v1/agent/chat` | Agent 多轮对话与草稿确认 |

除注册、登录、`/healthz` 和 `/readyz` 外均需携带：

```http
Authorization: Bearer <access_token>
```

仓库内接口概要见 [`docs/API.md`](docs/API.md)。完整字段仍以招新 Apifox 原文为准。

`APP_ENV=production` 时创建 `admin` 账户必须额外提供请求头：

```http
X-Admin-Registration-Secret: <ADMIN_REGISTRATION_SECRET>
```

## Agent 行为

- 未配置 LLM 时，接口仍提供本地只读查询回复；草稿请求返回 503，避免把用户指令误当作帖子正文。
- 配置 LLM 后，帖子和评论查询通过 Eino Tool 执行，最多连续调用三轮、累计八次工具调用。
- 请求起草帖子时只返回 `pending_action`，不会直接发布。
- 后续请求显式传入 `confirm_draft_id` 后才会创建帖子。
- 草稿绑定当前用户与 `session_id`，30 分钟后过期，且只能确认一次。
- 用户与助手消息保存失败时接口不会伪装成功；模型或 Tool 故障返回 503。
- 历史按消息数量和字符预算截断，旧消息与过期草稿按配置定期清理。

## 数据一致性与缓存

- 启动时持有 MySQL advisory lock 执行幂等、带版本记录的迁移，记录保存在 `schema_migrations`，多实例不会并发修改结构。
- 评论、点赞和 Agent 数据均有外键约束；删除帖子时由外键级联清理评论和点赞。
- 帖子互动计数、热门排序汇总信号与评论/点赞写入处于同一事务。列表展示使用持久计数；热门排序读取独立的非作者点赞者、独立评论者和互动时间汇总，并只扫描近期窗口的覆盖索引，作者自赞、自评不会提高排名。
- 帖子详情默认返回前 50 条评论，最多 100 条；使用响应中的 `comments_meta.next_cursor` 请求下一页，避免热门帖子一次加载全部评论。
- MySQL 是点赞权威数据源；Redis 只做缓存，故障不会阻断核心功能。
- Redis 点赞状态缓存使用短 TTL；删除帖子后接口会先通过 MySQL 校验帖子有效性，不会返回残留缓存。限流在 Redis 不可用时回退本机内存，客户端会收到精确到秒的 `Retry-After`。

## 验证

常规验证：

```powershell
go test -count=1 ./...
go vet ./...
docker compose config --quiet
docker build -t jinghong-forum-backend:test .
```

CI 还会运行竞态检测、隔离 MySQL/Redis 的 11 接口集成测试以及 `govulncheck`。

## 生产配置

设置 `APP_ENV=production` 后，服务会拒绝默认 MySQL 凭据、默认或过短的 `JWT_SECRET`、少于 16 字节的 `ADMIN_REGISTRATION_SECRET` 以及 `CORS_ORIGINS=*`。生产环境应设置完整的 `MYSQL_DSN`，并让 `MYSQL_USER`、`MYSQL_PASSWORD`、`MYSQL_DATABASE` 与 MySQL 容器保持一致；Redis 可通过 `REDIS_PASSWORD` 和 `REDIS_TLS` 加固。部署到反向代理后，应通过 `TRUSTED_PROXIES` 配置代理 IP/CIDR，多个值使用逗号分隔。

限流、Agent 保留时间、热门排序衰减参数、日志级别和轮转策略均可在 [`.env.example`](.env.example) 中调整。所有时长使用 Go duration 格式（如 `24h`），格式错误或非正值会阻止启动，不会静默回退。热门排序默认采用 `gravity=1.2`、基础热度降至一半的时间 `72h`、近期互动降至一半的时间 `24h`，近期互动查询窗口采用 Discourse 风格的 `168h`；修改前应根据实际发帖和互动周期复核。`/healthz` 用于存活检查，`/readyz` 检查必需的 MySQL，并报告 Redis 是否降级。
