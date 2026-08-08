package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Server struct {
	cfg           Config
	db            *gorm.DB
	redis         *redis.Client
	router        *gin.Engine
	http          *http.Server
	generate      func(context.Context, []*schema.Message) (*schema.Message, error)
	generateDraft func(context.Context, []*schema.Message) (*schema.Message, error)
	tools         *compose.ToolsNode
	sqlDB         *sql.DB
	limiter       *rateLimiter
	cleanupCancel context.CancelFunc
	cleanupDone   chan struct{}
	redisWarnMu   sync.Mutex
	redisWarnAt   time.Time
}

type claims struct {
	Role         string `json:"role"`
	TokenVersion uint64 `json:"token_version"`
	jwt.RegisteredClaims
}

type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func NewServer(ctx context.Context, cfg Config) (*Server, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	var db *gorm.DB
	var sqlDB *sql.DB
	var err error
	for i := 0; i < 20; i++ {
		db, err = gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
		if err == nil {
			sqlDB, err = db.DB()
		}
		if err == nil {
			sqlDB.SetMaxOpenConns(30)
			sqlDB.SetMaxIdleConns(10)
			sqlDB.SetConnMaxIdleTime(5 * time.Minute)
			sqlDB.SetConnMaxLifetime(30 * time.Minute)
			pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
			err = sqlDB.PingContext(pingCtx)
			pingCancel()
			if err == nil {
				break
			}
			_ = sqlDB.Close()
			sqlDB = nil
		}
		if i < 19 {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("connect mysql: %w", ctx.Err())
			case <-timer.C:
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("connect mysql: %w", err)
	}
	initialized := false
	var redisToClose *redis.Client
	defer func() {
		if initialized {
			return
		}
		if redisToClose != nil {
			_ = redisToClose.Close()
		}
		_ = sqlDB.Close()
	}()
	if err := migrateDatabase(ctx, db); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	s := &Server{cfg: cfg, db: db, sqlDB: sqlDB, limiter: newRateLimiter()}
	if cfg.RedisAddr != "" {
		redisOptions := &redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB}
		if cfg.RedisTLS {
			redisOptions.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		rdb := redis.NewClient(redisOptions)
		redisToClose = rdb
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		s.redis = rdb
		pingErr := rdb.Ping(pingCtx).Err()
		cancel()
		if pingErr != nil {
			slog.Warn("redis unavailable; falling back to mysql", "error", pingErr)
		}
	}
	if cfg.LLMAPIKey != "" {
		modelCfg := &openai.ChatModelConfig{APIKey: cfg.LLMAPIKey, Model: cfg.LLMModel}
		if cfg.LLMBaseURL != "" {
			modelCfg.BaseURL = cfg.LLMBaseURL
		}
		chatModel, modelErr := openai.NewChatModel(ctx, modelCfg)
		if modelErr != nil {
			return nil, fmt.Errorf("initialize eino model: %w", modelErr)
		} else {
			draftModel, draftErr := openai.NewChatModel(ctx, modelCfg)
			if draftErr != nil {
				return nil, fmt.Errorf("initialize eino draft model: %w", draftErr)
			}
			s.generateDraft = func(callCtx context.Context, messages []*schema.Message) (*schema.Message, error) {
				return draftModel.Generate(callCtx, messages)
			}
			agentTools, toolsErr := s.newAgentTools()
			if toolsErr == nil {
				infos := make([]*schema.ToolInfo, 0, len(agentTools))
				for _, agentTool := range agentTools {
					info, infoErr := agentTool.Info(ctx)
					if infoErr != nil {
						toolsErr = infoErr
						break
					}
					infos = append(infos, info)
				}
				if toolsErr == nil {
					toolsErr = chatModel.BindTools(infos)
				}
				if toolsErr == nil {
					s.tools, toolsErr = compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: agentTools})
				}
			}
			if toolsErr != nil {
				return nil, fmt.Errorf("initialize eino tools: %w", toolsErr)
			}
			s.generate = func(callCtx context.Context, messages []*schema.Message) (*schema.Message, error) {
				return chatModel.Generate(callCtx, messages)
			}
		}
	}
	if err := s.routes(); err != nil {
		return nil, err
	}
	s.http = &http.Server{Addr: ":" + cfg.Port, Handler: s.router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	s.cleanupCancel = cleanupCancel
	s.cleanupDone = make(chan struct{})
	go func() {
		defer close(s.cleanupDone)
		s.runAgentCleanup(cleanupCtx)
	}()
	initialized = true
	return s, nil
}

func (s *Server) Start() error {
	slog.Info("server listening", "address", s.http.Addr)
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	var result error
	if s.http != nil {
		result = s.http.Shutdown(ctx)
	}
	if s.cleanupCancel != nil {
		s.cleanupCancel()
	}
	if s.cleanupDone != nil {
		select {
		case <-s.cleanupDone:
		case <-ctx.Done():
			result = errors.Join(result, ctx.Err())
		}
	}
	if s.redis != nil {
		result = errors.Join(result, s.redis.Close())
	}
	if s.sqlDB != nil {
		result = errors.Join(result, s.sqlDB.Close())
	}
	return result
}

func (s *Server) routes() error {
	r := gin.New()
	var proxies []string
	for _, raw := range strings.Split(s.cfg.TrustedProxies, ",") {
		if proxy := strings.TrimSpace(raw); proxy != "" {
			proxies = append(proxies, proxy)
		}
	}
	if err := r.SetTrustedProxies(proxies); err != nil {
		return fmt.Errorf("configure trusted proxies: %w", err)
	}
	r.Use(s.requestLog(), s.recovery(), s.cors())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, envelope{0, "success", gin.H{"status": "ok"}}) })
	r.GET("/readyz", s.readiness)
	r.POST("/api/v1/auth/register", s.limitRequests("auth", s.cfg.AuthRateLimit, false), s.register)
	r.POST("/api/v1/auth/login", s.limitRequests("auth", s.cfg.AuthRateLimit, false), s.login)

	auth := r.Group("/api/v1", s.limitRequests("api", s.cfg.APIRateLimit, false), s.authenticate())
	auth.POST("/posts", s.limitRequests("write", s.cfg.WriteRateLimit, true), s.createPost)
	auth.GET("/posts", s.listPosts)
	auth.GET("/posts/:post_id", s.getPost)
	auth.DELETE("/posts/:post_id", s.limitRequests("write", s.cfg.WriteRateLimit, true), s.deleteOwnPost)
	auth.POST("/posts/:post_id/like", s.limitRequests("write", s.cfg.WriteRateLimit, true), s.toggleLike)
	auth.POST("/posts/likes", s.likeStatuses)
	auth.POST("/posts/:post_id/comment", s.limitRequests("write", s.cfg.WriteRateLimit, true), s.createComment)
	auth.POST("/agent/chat", s.limitRequests("agent", s.cfg.AgentRateLimit, true), s.agentChat)
	auth.DELETE("/admin/posts/:post_id", s.requireAdmin(), s.limitRequests("write", s.cfg.WriteRateLimit, true), s.adminDeletePost)
	s.router = r
	return nil
}

func (s *Server) readiness(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c, 2*time.Second)
	defer cancel()
	if s.sqlDB == nil || s.sqlDB.PingContext(ctx) != nil {
		fail(c, http.StatusServiceUnavailable, "数据库未就绪")
		return
	}
	data := gin.H{"status": "ready", "mysql": "ok", "redis": "disabled"}
	if s.redis != nil {
		if err := s.redis.Ping(ctx).Err(); err != nil {
			data["redis"] = "degraded"
		} else {
			data["redis"] = "ok"
		}
	}
	ok(c, http.StatusOK, data)
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("panic recovered", "panic", recovered, "stack", string(debug.Stack()), "request_id", c.GetString("request_id"), "method", c.Request.Method, "path", c.Request.URL.Path)
				if !c.Writer.Written() {
					fail(c, http.StatusInternalServerError, "服务器内部错误")
				} else {
					c.Abort()
				}
			}
		}()
		c.Next()
	}
}

func (s *Server) cors() gin.HandlerFunc {
	allowed := strings.TrimSpace(s.cfg.CORSOrigins)
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if allowed == "*" {
			c.Header("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			for _, candidate := range strings.Split(allowed, ",") {
				if strings.TrimSpace(candidate) == origin {
					c.Header("Access-Control-Allow-Origin", origin)
					c.Header("Vary", "Origin")
					break
				}
			}
		}
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Admin-Registration-Secret, X-Request-ID")
		c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func (s *Server) requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if !validRequestID(requestID) {
			requestID = newRequestID()
		}
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		start := time.Now()
		c.Next()
		uid, _ := c.Get("user_id")
		if c.Writer.Status() < http.StatusBadRequest && (c.Request.URL.Path == "/healthz" || c.Request.URL.Path == "/readyz") {
			return
		}
		slog.Info("http request", "request_id", requestID, "method", c.Request.Method, "path", c.Request.URL.Path, "status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds(), "user_id", uid)
	}
}

func ok(c *gin.Context, status int, data any) { c.JSON(status, envelope{0, "success", data}) }
func fail(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, envelope{status, msg, nil})
}

func failInternal(c *gin.Context, operation string, err error) {
	slog.Error("request failed", "request_id", c.GetString("request_id"), "operation", operation, "error", err, "method", c.Request.Method, "path", c.Request.URL.Path)
	fail(c, http.StatusInternalServerError, "服务器内部错误")
}

func bindJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return false
	}
	return true
}

func userID(c *gin.Context) uint64 { return c.MustGet("user_id").(uint64) }

func parseID(c *gin.Context, name string) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return 0, false
	}
	return id, true
}

func validContent(value string, max int) bool {
	n := utf8.RuneCountInString(value)
	return n >= 1 && n <= max && strings.TrimSpace(value) != ""
}

func (s *Server) createPost(c *gin.Context) {
	var req struct {
		Content string `json:"content"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if !validContent(req.Content, 2000) {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return
	}
	post := Post{UserID: userID(c), Content: req.Content}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&post).Error; err != nil {
			return err
		}
		return tx.Preload("Author").First(&post, post.ID).Error
	}); err != nil {
		failInternal(c, "create post", err)
		return
	}
	ok(c, http.StatusCreated, gin.H{"id": post.ID, "content": post.Content, "author": publicUser(post.Author), "created_at": post.CreatedAt})
}

const maxListOffset int64 = 10000

func (s *Server) listPosts(c *gin.Context) {
	pageValue, pageErr := strconv.ParseInt(c.DefaultQuery("page", "1"), 10, 32)
	pageSizeValue, pageSizeErr := strconv.ParseInt(c.DefaultQuery("page_size", "20"), 10, 32)
	order := c.DefaultQuery("sort", "latest")
	offset := (pageValue - 1) * pageSizeValue
	if pageErr != nil || pageSizeErr != nil || pageValue < 1 || pageValue > 100000 || pageSizeValue < 1 || pageSizeValue > 100 || offset < 0 || offset > maxListOffset || (order != "latest" && order != "hot") {
		fail(c, 400, "参数校验失败")
		return
	}
	page, pageSize := int(pageValue), int(pageSizeValue)
	var total int64
	var posts []Post
	rankingAt := time.Now()
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Post{}).Count(&total).Error; err != nil {
			return err
		}
		if order == "hot" {
			ids, err := s.hotPostIDs(tx, rankingAt, pageSize, int(offset))
			if err != nil {
				return err
			}
			posts, err = loadPostsInOrder(tx, ids)
			return err
		}
		return tx.Preload("Author").Limit(pageSize).Offset(int(offset)).Order("posts.created_at DESC").Order("posts.id DESC").Find(&posts).Error
	}); err != nil {
		failInternal(c, "list posts", err)
		return
	}
	items := make([]gin.H, 0, len(posts))
	for _, post := range posts {
		items = append(items, postData(post, post.LikeCount, post.CommentCount))
	}
	ok(c, 200, gin.H{"items": items, "meta": gin.H{"page": page, "page_size": pageSize, "total": total}})
}

func postData(post Post, likeCount, commentCount int64) gin.H {
	return gin.H{"id": post.ID, "content": post.Content, "author": publicUser(post.Author), "like_count": likeCount, "comment_count": commentCount, "created_at": post.CreatedAt}
}

func (s *Server) counts(ctx context.Context, posts []Post) (map[uint64]int64, map[uint64]int64, error) {
	likes, comments := map[uint64]int64{}, map[uint64]int64{}
	for _, post := range posts {
		likes[post.ID] = post.LikeCount
		comments[post.ID] = post.CommentCount
	}
	return likes, comments, nil
}

func (s *Server) getPost(c *gin.Context) {
	id, valid := parseID(c, "post_id")
	if !valid {
		return
	}
	commentLimitValue, limitErr := strconv.ParseInt(c.DefaultQuery("comments_limit", "50"), 10, 32)
	afterCommentID, cursorErr := strconv.ParseUint(c.DefaultQuery("after_comment_id", "0"), 10, 64)
	if limitErr != nil || cursorErr != nil || commentLimitValue < 1 || commentLimitValue > 100 {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return
	}
	commentLimit := int(commentLimitValue)
	var post Post
	var comments []Comment
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Preload("Author").First(&post, id).Error; err != nil {
			return err
		}
		query := tx.Preload("Author").Where("post_id = ?", id)
		if afterCommentID > 0 {
			query = query.Where("id > ?", afterCommentID)
		}
		return query.Order("id ASC").Limit(commentLimit + 1).Find(&comments).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "帖子不存在")
		return
	} else if err != nil {
		failInternal(c, "get post", err)
		return
	}
	hasMore := len(comments) > commentLimit
	if hasMore {
		comments = comments[:commentLimit]
	}
	result := postData(post, post.LikeCount, post.CommentCount)
	commentData := make([]gin.H, 0, len(comments))
	for _, comment := range comments {
		commentData = append(commentData, gin.H{"id": comment.ID, "post_id": comment.PostID, "content": comment.Content, "author": publicUser(comment.Author), "created_at": comment.CreatedAt})
	}
	result["comments"] = commentData
	nextCursor := uint64(0)
	if len(comments) > 0 {
		nextCursor = comments[len(comments)-1].ID
	}
	result["comments_meta"] = gin.H{"limit": commentLimit, "next_cursor": nextCursor, "has_more": hasMore}
	ok(c, 200, result)
}

var errForbiddenPostDelete = errors.New("forbidden post delete")

func (s *Server) deletePost(id uint64, ownerID uint64, enforceOwner bool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var post Post
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "user_id").First(&post, id).Error; err != nil {
			return err
		}
		if enforceOwner && post.UserID != ownerID {
			return errForbiddenPostDelete
		}
		return tx.Delete(&post).Error
	})
}

func (s *Server) deleteOwnPost(c *gin.Context) {
	id, valid := parseID(c, "post_id")
	if !valid {
		return
	}
	err := s.deletePost(id, userID(c), true)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "帖子不存在")
		return
	}
	if errors.Is(err, errForbiddenPostDelete) {
		fail(c, 403, "无权删除他人的帖子")
		return
	}
	if err != nil {
		failInternal(c, "delete own post", err)
		return
	}
	slog.Info("post deleted by owner", "request_id", c.GetString("request_id"), "user_id", userID(c), "post_id", id)
	ok(c, 200, nil)
}

func (s *Server) adminDeletePost(c *gin.Context) {
	id, valid := parseID(c, "post_id")
	if !valid {
		return
	}
	err := s.deletePost(id, 0, false)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "帖子不存在")
		return
	}
	if err != nil {
		failInternal(c, "admin delete post", err)
		return
	}
	slog.Info("post deleted by administrator", "request_id", c.GetString("request_id"), "admin_user_id", userID(c), "post_id", id)
	ok(c, 200, nil)
}

func (s *Server) toggleLike(c *gin.Context) {
	postID, valid := parseID(c, "post_id")
	if !valid {
		return
	}
	uid := userID(c)
	liked := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var post Post
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "user_id").First(&post, postID).Error; err != nil {
			return err
		}
		var like PostLike
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("post_id = ? AND user_id = ?", postID, uid).First(&like).Error
		if err == nil {
			liked = false
			if err := tx.Delete(&like).Error; err != nil {
				return err
			}
			updates := map[string]any{"like_count": gorm.Expr("GREATEST(like_count - 1, 0)")}
			if uid != post.UserID {
				updates["external_like_count"] = gorm.Expr("GREATEST(external_like_count - 1, 0)")
			}
			if err := tx.Model(&Post{}).Where("id = ?", postID).UpdateColumns(updates).Error; err != nil {
				return err
			}
			if uid != post.UserID {
				return refreshLastExternalActivity(tx, postID, post.UserID)
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		liked = true
		like = PostLike{PostID: postID, UserID: uid}
		if err := tx.Create(&like).Error; err != nil {
			return err
		}
		updates := map[string]any{"like_count": gorm.Expr("like_count + 1")}
		if uid != post.UserID {
			updates["external_like_count"] = gorm.Expr("external_like_count + 1")
			updates["last_external_activity_at"] = like.CreatedAt
		}
		return tx.Model(&Post{}).Where("id = ?", postID).UpdateColumns(updates).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "帖子不存在")
		return
	}
	if err != nil {
		failInternal(c, "toggle post like", err)
		return
	}
	s.cacheLike(c, uid, postID, liked)
	ok(c, 200, gin.H{"post_id": postID, "is_liked": liked})
}

func refreshLastExternalActivity(tx *gorm.DB, postID, authorID uint64) error {
	var result struct {
		LastActivityAt *time.Time `gorm:"column:last_activity_at"`
	}
	if err := tx.Raw(`SELECT MAX(activity_at) AS last_activity_at
		FROM (
			SELECT MAX(created_at) AS activity_at FROM post_likes WHERE post_id = ? AND user_id <> ?
			UNION ALL
			SELECT MAX(created_at) AS activity_at FROM comments WHERE post_id = ? AND user_id <> ?
		) AS external_activities`, postID, authorID, postID, authorID).Scan(&result).Error; err != nil {
		return err
	}
	return tx.Model(&Post{}).Where("id = ?", postID).UpdateColumn("last_external_activity_at", result.LastActivityAt).Error
}

func (s *Server) cacheLike(ctx context.Context, uid, postID uint64, liked bool) {
	if s.redis == nil {
		return
	}
	key := fmt.Sprintf("forum:like:%d:%d", uid, postID)
	if err := s.redis.Set(ctx, key, strconv.FormatBool(liked), 15*time.Minute).Err(); err != nil {
		s.warnRedis("cache like status failed", "error", err, "user_id", uid, "post_id", postID)
	}
}

func (s *Server) cacheLikeStatuses(ctx context.Context, uid uint64, statuses map[uint64]bool) {
	if s.redis == nil || len(statuses) == 0 {
		return
	}
	pipe := s.redis.Pipeline()
	for postID, liked := range statuses {
		key := fmt.Sprintf("forum:like:%d:%d", uid, postID)
		pipe.Set(ctx, key, strconv.FormatBool(liked), 15*time.Minute)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		s.warnRedis("cache like statuses failed", "error", err, "user_id", uid, "count", len(statuses))
	}
}

func (s *Server) likeStatuses(c *gin.Context) {
	var req struct {
		PostIDs []uint64 `json:"post_ids"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if len(req.PostIDs) == 0 || len(req.PostIDs) > 100 {
		fail(c, 400, "参数校验失败")
		return
	}
	seen := map[uint64]bool{}
	for _, id := range req.PostIDs {
		if id == 0 || seen[id] {
			fail(c, 400, "参数校验失败")
			return
		}
		seen[id] = true
	}
	var validCount int64
	if err := s.db.Model(&Post{}).Where("id IN ?", req.PostIDs).Count(&validCount).Error; err != nil {
		failInternal(c, "validate like status posts", err)
		return
	}
	if validCount != int64(len(req.PostIDs)) {
		fail(c, 400, "参数校验失败")
		return
	}
	liked := map[uint64]bool{}
	missing := append([]uint64(nil), req.PostIDs...)
	if s.redis != nil {
		keys := make([]string, 0, len(req.PostIDs))
		for _, id := range req.PostIDs {
			keys = append(keys, fmt.Sprintf("forum:like:%d:%d", userID(c), id))
		}
		values, err := s.redis.MGet(c, keys...).Result()
		if err == nil {
			missing = missing[:0]
			for i, value := range values {
				if text, ok := value.(string); ok {
					parsed, parseErr := strconv.ParseBool(text)
					if parseErr == nil {
						liked[req.PostIDs[i]] = parsed
						continue
					}
				}
				missing = append(missing, req.PostIDs[i])
			}
		} else {
			s.warnRedis("load like status cache failed", "error", err, "user_id", userID(c))
		}
	}
	if len(missing) > 0 {
		var likes []PostLike
		if err := s.db.Where("user_id = ? AND post_id IN ?", userID(c), missing).Find(&likes).Error; err != nil {
			failInternal(c, "load like statuses", err)
			return
		}
		for _, like := range likes {
			liked[like.PostID] = true
		}
		for _, id := range missing {
			if _, exists := liked[id]; !exists {
				liked[id] = false
			}
		}
	}
	s.cacheLikeStatuses(c, userID(c), liked)
	status := make([]gin.H, 0, len(req.PostIDs))
	for _, id := range req.PostIDs {
		status = append(status, gin.H{"post_id": id, "liked": liked[id]})
	}
	ok(c, 200, gin.H{"status": status})
}

func (s *Server) createComment(c *gin.Context) {
	postID, valid := parseID(c, "post_id")
	if !valid {
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if !validContent(req.Content, 1000) {
		fail(c, 400, "参数校验失败")
		return
	}
	comment := Comment{PostID: postID, UserID: userID(c), Content: req.Content}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var post Post
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "user_id").First(&post, postID).Error; err != nil {
			return err
		}
		firstExternalComment := false
		if comment.UserID != post.UserID {
			var existing Comment
			err := tx.Select("id").Where("post_id = ? AND user_id = ?", postID, comment.UserID).Take(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			firstExternalComment = errors.Is(err, gorm.ErrRecordNotFound)
		}
		if err := tx.Create(&comment).Error; err != nil {
			return err
		}
		updates := map[string]any{"comment_count": gorm.Expr("comment_count + 1")}
		if comment.UserID != post.UserID {
			updates["last_external_activity_at"] = comment.CreatedAt
			if firstExternalComment {
				updates["external_commenter_count"] = gorm.Expr("external_commenter_count + 1")
			}
		}
		if err := tx.Model(&Post{}).Where("id = ?", postID).UpdateColumns(updates).Error; err != nil {
			return err
		}
		return tx.Preload("Author").First(&comment, comment.ID).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) || isForeignKeyViolation(err) {
		fail(c, 404, "帖子不存在")
		return
	}
	if err != nil {
		failInternal(c, "create comment", err)
		return
	}
	ok(c, 201, gin.H{"id": comment.ID, "post_id": comment.PostID, "content": comment.Content, "author": publicUser(comment.Author), "created_at": comment.CreatedAt})
}
