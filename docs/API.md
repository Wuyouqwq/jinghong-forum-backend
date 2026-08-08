# API 接口概要

基础地址：`/api/v1`。除注册、登录、`/healthz` 和 `/readyz` 外，接口均需携带：

```http
Authorization: Bearer <access_token>
```

统一成功响应：

```json
{"code": 0, "msg": "success", "data": {}}
```

统一错误响应：

```json
{"code": 400, "msg": "错误说明", "data": null}
```

| 方法 | 路径 | 主要请求字段 | 成功状态 |
|---|---|---|---|
| POST | `/auth/register` | `username,name,password,role` | 201 |
| POST | `/auth/login` | `username,password` | 200 |
| POST | `/posts` | `content` | 201 |
| GET | `/posts` | `page,page_size,sort=latest\|hot` | 200 |
| GET | `/posts/{post_id}` | `comments_limit=1..100,after_comment_id` | 200 |
| DELETE | `/posts/{post_id}` | - | 200 |
| POST | `/posts/{post_id}/like` | - | 200 |
| POST | `/posts/likes` | `post_ids` | 200 |
| POST | `/posts/{post_id}/comment` | `content` | 201 |
| DELETE | `/admin/posts/{post_id}` | - | 200 |
| POST | `/agent/chat` | `session_id,message,confirm_draft_id?` | 200 |

`POST /posts/likes` 成功响应遵循 Apifox JSON Schema，数组字段为 `data.status`。原 Apifox 示例中的 `data.statuses` 与其 Schema 不一致，本项目以 Schema 为准并通过集成测试锁定该字段。

公共运行状态端点：`GET /healthz` 为进程存活检查，`GET /readyz` 检查 MySQL 就绪状态并返回 Redis 的 `ok`、`degraded` 或 `disabled` 状态。

所有受限接口可能返回 `429`，并通过 `Retry-After` 提示重试时间。Agent 模型或 Tool 暂时不可用时返回 `503`，统一响应结构不变。

## 进阶行为

- 点赞权威状态同步写入 MySQL，Redis 只缓存查询结果，缓存故障时自动回源。
- 帖子列表支持最新和人性化热门排序。热门分由整体认可度和近期升温度组成：只统计非作者的独立点赞者和独立评论者，使用对数压缩及可配置的幂函数半衰期；作者自赞、自评和同一用户重复评论不会抬高排名，真实近期互动可以让旧帖重新升温。累计信号随写事务维护，近期互动使用时间前导覆盖索引，避免每次列表请求全量重算历史互动。
- 帖子详情的 `comments` 使用游标分页，默认返回 50 条、最多 100 条；响应的 `comments_meta` 包含 `limit`、`next_cursor` 和 `has_more`，下一页把 `next_cursor` 作为 `after_comment_id`。
- 传统页码分页最多允许跳过 10000 条记录，超出返回 400，避免深 `OFFSET` 持续占用数据库。
- Agent 帖子关键词查询使用 MySQL ngram FULLTEXT 索引，不再执行前置通配符全表扫描。
- Agent 使用 `session_id` 和当前用户隔离历史消息。
- Agent 通过 Eino Tool 查询帖子和评论，Tool 结果回填模型后再生成回答。
- 未配置 LLM 时仍可使用本地只读查询回复；草稿生成返回 503，不会把用户的操作指令直接发布为正文。
- 创建帖子第一次只返回有时限的草稿；只有后续显式传入 `confirm_draft_id` 才会在事务中发布。
- `confirm_draft_id` 请求本身及确认回复都会写入对应会话历史。

## 管理员注册

开发环境保持原始接口格式。`APP_ENV=production` 时必须额外携带：

```http
X-Admin-Registration-Secret: <ADMIN_REGISTRATION_SECRET>
```

完整字段长度、示例和状态码以招新 Apifox 原文为准。
