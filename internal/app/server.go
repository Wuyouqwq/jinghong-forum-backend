package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var usernamePattern = regexp.MustCompile(`^[0-9]{1,32}$`)

type Server struct {
	cfg      Config
	db       *gorm.DB
	redis    *redis.Client
	router   *gin.Engine
	http     *http.Server
	generate func(context.Context, []*schema.Message) (*schema.Message, error)
	tools    *compose.ToolsNode
}

type claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func NewServer(ctx context.Context, cfg Config) (*Server, error) {
	var db *gorm.DB
	var err error
	for i := 0; i < 20; i++ {
		db, err = gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("connect mysql: %w", err)
	}
	if err := db.AutoMigrate(&User{}, &Post{}, &Comment{}, &PostLike{}, &AgentMessage{}, &AgentDraft{}); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	s := &Server{cfg: cfg, db: db}
	if cfg.RedisAddr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB})
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := rdb.Ping(pingCtx).Err(); err == nil {
			s.redis = rdb
		} else {
			slog.Warn("redis unavailable; falling back to mysql", "error", err)
			_ = rdb.Close()
		}
	}
	if cfg.LLMAPIKey != "" {
		modelCfg := &openai.ChatModelConfig{APIKey: cfg.LLMAPIKey, Model: cfg.LLMModel}
		if cfg.LLMBaseURL != "" {
			modelCfg.BaseURL = cfg.LLMBaseURL
		}
		chatModel, modelErr := openai.NewChatModel(ctx, modelCfg)
		if modelErr != nil {
			slog.Warn("eino model unavailable", "error", modelErr)
		} else {
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
				slog.Warn("eino tools unavailable", "error", toolsErr)
			}
			s.generate = func(callCtx context.Context, messages []*schema.Message) (*schema.Message, error) {
				return chatModel.Generate(callCtx, messages)
			}
		}
	}
	s.routes()
	s.http = &http.Server{Addr: ":" + cfg.Port, Handler: s.router, ReadHeaderTimeout: 5 * time.Second}
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
	if s.redis != nil {
		_ = s.redis.Close()
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) routes() {
	r := gin.New()
	r.Use(gin.Recovery(), s.cors(), s.requestLog())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, envelope{0, "success", gin.H{"status": "ok"}}) })
	r.POST("/api/v1/auth/register", s.register)
	r.POST("/api/v1/auth/login", s.login)

	auth := r.Group("/api/v1", s.authenticate())
	auth.POST("/posts", s.createPost)
	auth.GET("/posts", s.listPosts)
	auth.GET("/posts/:post_id", s.getPost)
	auth.DELETE("/posts/:post_id", s.deleteOwnPost)
	auth.POST("/posts/:post_id/like", s.toggleLike)
	auth.POST("/posts/likes", s.likeStatuses)
	auth.POST("/posts/:post_id/comment", s.createComment)
	auth.POST("/agent/chat", s.agentChat)
	auth.DELETE("/admin/posts/:post_id", s.requireAdmin(), s.adminDeletePost)
	s.router = r
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
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
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
		start := time.Now()
		c.Next()
		slog.Info("http request", "method", c.Request.Method, "path", c.Request.URL.Path, "status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds())
	}
}

func ok(c *gin.Context, status int, data any) { c.JSON(status, envelope{0, "success", data}) }
func fail(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, envelope{status, msg, nil})
}

func failInternal(c *gin.Context, operation string, err error) {
	slog.Error("request failed", "operation", operation, "error", err, "method", c.Request.Method, "path", c.Request.URL.Path)
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

func (s *Server) authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(c, http.StatusUnauthorized, "未登录或令牌无效")
			return
		}
		token, err := jwt.ParseWithClaims(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")), &claims{}, func(token *jwt.Token) (any, error) {
			if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(s.cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			fail(c, http.StatusUnauthorized, "未登录或令牌无效")
			return
		}
		cl, valid := token.Claims.(*claims)
		id, parseErr := strconv.ParseUint(cl.Subject, 10, 64)
		if !valid || parseErr != nil || id == 0 {
			fail(c, http.StatusUnauthorized, "未登录或令牌无效")
			return
		}
		c.Set("user_id", id)
		c.Set("role", cl.Role)
		c.Next()
	}
}

func (s *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("role") != "admin" {
			fail(c, http.StatusForbidden, "仅管理员可执行此操作")
			return
		}
		c.Next()
	}
}

func publicUser(user User) gin.H {
	return gin.H{"id": user.ID, "username": user.Username, "name": user.Name, "role": user.Role}
}

func (s *Server) register(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if !usernamePattern.MatchString(req.Username) || utf8.RuneCountInString(req.Name) < 1 || utf8.RuneCountInString(req.Name) > 32 || len(req.Password) < 8 || len(req.Password) > 16 || (req.Role != "student" && req.Role != "admin") {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return
	}
	if req.Role == "admin" {
		provided := c.GetHeader("X-Admin-Registration-Secret")
		expected := s.cfg.AdminRegistrationSecret
		if expected == "" || len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			fail(c, http.StatusForbidden, "禁止创建管理员账户")
			return
		}
	}
	var count int64
	s.db.Model(&User{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		fail(c, http.StatusConflict, "用户名已存在")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		failInternal(c, "hash password", err)
		return
	}
	user := User{Username: req.Username, Name: req.Name, PasswordHash: string(hash), Role: req.Role}
	if err := s.db.Create(&user).Error; err != nil {
		fail(c, http.StatusConflict, "用户名已存在")
		return
	}
	ok(c, http.StatusCreated, publicUser(user))
}

func (s *Server) login(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.Username == "" || req.Password == "" {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return
	}
	var user User
	if err := s.db.Where("username = ?", req.Username).First(&user).Error; err != nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		fail(c, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{Role: user.Role, RegisteredClaims: jwt.RegisteredClaims{Subject: strconv.FormatUint(user.ID, 10), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.JWTExpires))}})
	signed, err := token.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		failInternal(c, "sign jwt", err)
		return
	}
	ok(c, http.StatusOK, gin.H{"access_token": signed, "token_type": "Bearer", "expires_in": int64(s.cfg.JWTExpires.Seconds()), "user": publicUser(user)})
}

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
	if err := s.db.Create(&post).Error; err != nil {
		failInternal(c, "create post", err)
		return
	}
	if err := s.db.Preload("Author").First(&post, post.ID).Error; err != nil {
		failInternal(c, "load created post", err)
		return
	}
	ok(c, http.StatusCreated, gin.H{"id": post.ID, "content": post.Content, "author": publicUser(post.Author), "created_at": post.CreatedAt})
}

func (s *Server) listPosts(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	order := c.DefaultQuery("sort", "latest")
	if page < 1 || pageSize < 1 || pageSize > 100 || (order != "latest" && order != "hot") {
		fail(c, 400, "参数校验失败")
		return
	}
	var total int64
	s.db.Model(&Post{}).Count(&total)
	query := s.db.Preload("Author").Limit(pageSize).Offset((page - 1) * pageSize)
	if order == "hot" {
		query = query.Order("((SELECT COUNT(*) FROM post_likes WHERE post_likes.post_id = posts.id) * 3 + (SELECT COUNT(*) FROM comments WHERE comments.post_id = posts.id) * 2 - TIMESTAMPDIFF(HOUR, posts.created_at, NOW()) * 0.15) DESC").Order("posts.created_at DESC")
	} else {
		query = query.Order("posts.created_at DESC").Order("posts.id DESC")
	}
	var posts []Post
	if err := query.Find(&posts).Error; err != nil {
		failInternal(c, "list posts", err)
		return
	}
	likes, comments := s.counts(posts)
	items := make([]gin.H, 0, len(posts))
	for _, post := range posts {
		items = append(items, postData(post, likes[post.ID], comments[post.ID]))
	}
	ok(c, 200, gin.H{"items": items, "meta": gin.H{"page": page, "page_size": pageSize, "total": total}})
}

func postData(post Post, likeCount, commentCount int64) gin.H {
	return gin.H{"id": post.ID, "content": post.Content, "author": publicUser(post.Author), "like_count": likeCount, "comment_count": commentCount, "created_at": post.CreatedAt}
}

func (s *Server) counts(posts []Post) (map[uint64]int64, map[uint64]int64) {
	ids := make([]uint64, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	likes, comments := map[uint64]int64{}, map[uint64]int64{}
	if len(ids) == 0 {
		return likes, comments
	}
	type row struct {
		PostID uint64
		Count  int64
	}
	var rows []row
	s.db.Model(&PostLike{}).Select("post_id, count(*) as count").Where("post_id IN ?", ids).Group("post_id").Scan(&rows)
	for _, r := range rows {
		likes[r.PostID] = r.Count
	}
	rows = nil
	s.db.Model(&Comment{}).Select("post_id, count(*) as count").Where("post_id IN ?", ids).Group("post_id").Scan(&rows)
	for _, r := range rows {
		comments[r.PostID] = r.Count
	}
	return likes, comments
}

func (s *Server) getPost(c *gin.Context) {
	id, valid := parseID(c, "post_id")
	if !valid {
		return
	}
	var post Post
	if err := s.db.Preload("Author").First(&post, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "帖子不存在")
		return
	} else if err != nil {
		failInternal(c, "get post", err)
		return
	}
	var comments []Comment
	s.db.Preload("Author").Where("post_id = ?", id).Order("created_at ASC, id ASC").Find(&comments)
	var likeCount int64
	s.db.Model(&PostLike{}).Where("post_id = ?", id).Count(&likeCount)
	result := postData(post, likeCount, int64(len(comments)))
	commentData := make([]gin.H, 0, len(comments))
	for _, comment := range comments {
		commentData = append(commentData, gin.H{"id": comment.ID, "post_id": comment.PostID, "content": comment.Content, "author": publicUser(comment.Author), "created_at": comment.CreatedAt})
	}
	result["comments"] = commentData
	ok(c, 200, result)
}

func (s *Server) deletePost(id uint64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("post_id = ?", id).Delete(&PostLike{}).Error; err != nil {
			return err
		}
		if err := tx.Where("post_id = ?", id).Delete(&Comment{}).Error; err != nil {
			return err
		}
		return tx.Delete(&Post{}, id).Error
	})
}

func (s *Server) deleteOwnPost(c *gin.Context) {
	id, valid := parseID(c, "post_id")
	if !valid {
		return
	}
	var post Post
	if err := s.db.First(&post, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "帖子不存在")
		return
	} else if err != nil {
		failInternal(c, "load post for owner delete", err)
		return
	}
	if post.UserID != userID(c) {
		fail(c, 403, "无权删除他人的帖子")
		return
	}
	if err := s.deletePost(id); err != nil {
		failInternal(c, "delete own post", err)
		return
	}
	ok(c, 200, nil)
}

func (s *Server) adminDeletePost(c *gin.Context) {
	id, valid := parseID(c, "post_id")
	if !valid {
		return
	}
	var count int64
	s.db.Model(&Post{}).Where("id = ?", id).Count(&count)
	if count == 0 {
		fail(c, 404, "帖子不存在")
		return
	}
	if err := s.deletePost(id); err != nil {
		failInternal(c, "admin delete post", err)
		return
	}
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
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&post, postID).Error; err != nil {
			return err
		}
		var like PostLike
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("post_id = ? AND user_id = ?", postID, uid).First(&like).Error
		if err == nil {
			liked = false
			return tx.Delete(&like).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		liked = true
		return tx.Create(&PostLike{PostID: postID, UserID: uid}).Error
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

func (s *Server) cacheLike(c *gin.Context, uid, postID uint64, liked bool) {
	if s.redis == nil {
		return
	}
	key := fmt.Sprintf("forum:like:%d:%d", uid, postID)
	_ = s.redis.Set(c, key, strconv.FormatBool(liked), 15*time.Minute).Err()
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
	s.db.Model(&Post{}).Where("id IN ?", req.PostIDs).Count(&validCount)
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
					liked[req.PostIDs[i]], _ = strconv.ParseBool(text)
				} else {
					missing = append(missing, req.PostIDs[i])
				}
			}
		}
	}
	if len(missing) > 0 {
		var likes []PostLike
		s.db.Where("user_id = ? AND post_id IN ?", userID(c), missing).Find(&likes)
		for _, like := range likes {
			liked[like.PostID] = true
		}
		for _, id := range missing {
			s.cacheLike(c, userID(c), id, liked[id])
		}
	}
	status := make([]gin.H, 0, len(req.PostIDs))
	for _, id := range req.PostIDs {
		status = append(status, gin.H{"post_id": id, "liked": liked[id]})
		s.cacheLike(c, userID(c), id, liked[id])
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
	var postCount int64
	s.db.Model(&Post{}).Where("id = ?", postID).Count(&postCount)
	if postCount == 0 {
		fail(c, 404, "帖子不存在")
		return
	}
	comment := Comment{PostID: postID, UserID: userID(c), Content: req.Content}
	if err := s.db.Create(&comment).Error; err != nil {
		failInternal(c, "create comment", err)
		return
	}
	if err := s.db.Preload("Author").First(&comment, comment.ID).Error; err != nil {
		failInternal(c, "load created comment", err)
		return
	}
	ok(c, 201, gin.H{"id": comment.ID, "post_id": comment.PostID, "content": comment.Content, "author": publicUser(comment.Author), "created_at": comment.CreatedAt})
}

func newDraftID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "draft_" + hex.EncodeToString(b)
}

func (s *Server) agentChat(c *gin.Context) {
	var req struct {
		SessionID      string `json:"session_id"`
		Message        string `json:"message"`
		ConfirmDraftID string `json:"confirm_draft_id,omitempty"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if !validContent(req.SessionID, 128) || !validContent(req.Message, 4000) {
		fail(c, 400, "参数校验失败")
		return
	}
	uid := userID(c)
	if req.ConfirmDraftID != "" {
		s.confirmDraft(c, uid, req.SessionID, req.ConfirmDraftID)
		return
	}
	if err := s.db.Create(&AgentMessage{UserID: uid, SessionID: req.SessionID, Role: "user", Content: req.Message}).Error; err != nil {
		slog.Warn("save agent message failed", "role", "user", "error", err)
	}
	if (strings.Contains(req.Message, "帖子") || strings.Contains(req.Message, "发布")) && (strings.Contains(req.Message, "起草") || strings.Contains(req.Message, "写一条")) {
		content := req.Message
		if s.generate != nil {
			ctx, cancel := context.WithTimeout(c, 45*time.Second)
			defer cancel()
			msg, err := s.generate(ctx, []*schema.Message{schema.SystemMessage("你是论坛帖子编辑。只输出不超过2000字的帖子正文，不执行发布。"), schema.UserMessage(req.Message)})
			if err == nil && strings.TrimSpace(msg.Content) != "" {
				content = strings.TrimSpace(msg.Content)
			}
		}
		if utf8.RuneCountInString(content) > 2000 {
			content = string([]rune(content)[:2000])
		}
		draft := AgentDraft{ID: newDraftID(), UserID: uid, SessionID: req.SessionID, Action: "create_post", Content: content, ExpiresAt: time.Now().Add(30 * time.Minute)}
		if err := s.db.Create(&draft).Error; err != nil {
			failInternal(c, "create agent draft", err)
			return
		}
		reply := "我已为你生成帖子草稿。请确认后再发布。"
		if err := s.db.Create(&AgentMessage{UserID: uid, SessionID: req.SessionID, Role: "assistant", Content: reply}).Error; err != nil {
			slog.Warn("save agent message failed", "role", "assistant", "error", err)
		}
		ok(c, 200, gin.H{"session_id": req.SessionID, "reply": reply, "pending_action": gin.H{"draft_id": draft.ID, "action": draft.Action, "content": draft.Content, "expires_at": draft.ExpiresAt}})
		return
	}

	reply := s.agentReply(c, uid, req.SessionID, req.Message)
	if err := s.db.Create(&AgentMessage{UserID: uid, SessionID: req.SessionID, Role: "assistant", Content: reply}).Error; err != nil {
		slog.Warn("save agent message failed", "role", "assistant", "error", err)
	}
	ok(c, 200, gin.H{"session_id": req.SessionID, "reply": reply, "pending_action": nil})
}

func (s *Server) agentReply(c *gin.Context, uid uint64, sessionID, input string) string {
	if s.generate == nil {
		var posts []Post
		s.db.Preload("Author").Order("created_at DESC").Limit(5).Find(&posts)
		if len(posts) == 0 {
			return "当前还没有帖子。你可以让我起草一条新帖子。"
		}
		return fmt.Sprintf("当前共有最新帖子可供查询，最近一条由%s发布：%s", posts[0].Author.Name, posts[0].Content)
	}
	var history []AgentMessage
	s.db.Where("user_id = ? AND session_id = ?", uid, sessionID).Order("id DESC").Limit(12).Find(&history)
	messages := []*schema.Message{schema.SystemMessage("你是论坛助手。查询帖子或评论时必须调用提供的只读工具，不能编造查询结果。任何写操作只能建议用户请求草稿，不能声称已经执行。")}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			messages = append(messages, schema.AssistantMessage(history[i].Content, nil))
		} else {
			messages = append(messages, schema.UserMessage(history[i].Content))
		}
	}
	ctx, cancel := context.WithTimeout(c, 45*time.Second)
	defer cancel()
	for round := 0; round < 3; round++ {
		result, err := s.generate(ctx, messages)
		if err != nil {
			slog.Warn("eino generate failed", "error", err)
			return "Agent 服务暂时不可用，请稍后重试。"
		}
		messages = append(messages, result)
		if len(result.ToolCalls) == 0 {
			if strings.TrimSpace(result.Content) == "" {
				return "Agent 服务暂时不可用，请稍后重试。"
			}
			return result.Content
		}
		if s.tools == nil {
			slog.Warn("eino requested a tool but tools node is unavailable")
			return "Agent 查询工具暂时不可用，请稍后重试。"
		}
		toolMessages, err := s.tools.Invoke(ctx, result)
		if err != nil {
			slog.Warn("eino tool execution failed", "error", err)
			return "Agent 查询工具暂时不可用，请稍后重试。"
		}
		messages = append(messages, toolMessages...)
	}
	return "Agent 工具调用次数过多，请缩小查询范围后重试。"
}

func (s *Server) confirmDraft(c *gin.Context, uid uint64, sessionID, draftID string) {
	var draft AgentDraft
	err := s.db.Where("id = ? AND user_id = ? AND session_id = ?", draftID, uid, sessionID).First(&draft).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		failInternal(c, "load agent draft", err)
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || draft.ConfirmedAt != nil || time.Now().After(draft.ExpiresAt) {
		fail(c, 404, "待确认草稿不存在或已过期")
		return
	}
	var post Post
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var locked AgentDraft
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND confirmed_at IS NULL", draft.ID).First(&locked).Error; err != nil {
			return err
		}
		post = Post{UserID: uid, Content: locked.Content}
		if err := tx.Create(&post).Error; err != nil {
			return err
		}
		now := time.Now()
		return tx.Model(&locked).Update("confirmed_at", now).Error
	})
	if err != nil {
		fail(c, 400, "草稿确认无效")
		return
	}
	reply := fmt.Sprintf("帖子已发布，帖子 ID 为 %d。", post.ID)
	if err := s.db.Create(&AgentMessage{UserID: uid, SessionID: sessionID, Role: "assistant", Content: reply}).Error; err != nil {
		slog.Warn("save agent message failed", "role", "assistant", "error", err)
	}
	ok(c, 200, gin.H{"session_id": sessionID, "reply": reply, "pending_action": nil})
}
