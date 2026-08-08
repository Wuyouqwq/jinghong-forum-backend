# API 概要

基础地址：`/api/v1`。除注册、登录、`/healthz` 和 `/readyz` 外，均需携带：

```http
Authorization: Bearer <access_token>
```

统一响应：

```json
{"code": 0, "msg": "success", "data": {}}
```

错误响应使用非零 `code`，`data` 为 `null`。

| 方法 | 路径 | 主要请求字段 | 状态 |
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

`POST /posts/likes` 按 Apifox Schema 返回 `data.status`；原示例中的 `data.statuses` 与 Schema 不一致。

## 行为约束

- 帖子列表支持 `latest` 和 `hot`。热门排序排除作者自赞、自评，并按独立互动者、对数压缩和时间衰减计分。
- 评论默认返回 50 条、最多 100 条；下一页将 `comments_meta.next_cursor` 作为 `after_comment_id`。
- 页码分页最多跳过 10000 条记录，超出返回 `400`。
- 点赞以 MySQL 为准；Redis 仅作缓存，故障时自动回源。
- 所有受限接口可能返回 `429`，重试时间见 `Retry-After`。
- Agent 模型或 Tool 不可用时返回 `503`。
- Agent 历史按用户和 `session_id` 隔离；帖子先生成草稿，后续传入 `confirm_draft_id` 才会发布。

## 管理员注册

`APP_ENV=production` 时，注册 `admin` 必须额外携带：

```http
X-Admin-Registration-Secret: <ADMIN_REGISTRATION_SECRET>
```

## 健康检查

- `GET /healthz`：进程存活状态。
- `GET /readyz`：MySQL 就绪状态及 Redis 的 `ok`、`degraded` 或 `disabled` 状态。

完整字段长度、示例和状态码以招新 Apifox 文档为准。
