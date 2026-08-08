package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestAgentToolsCanBeRegistered(t *testing.T) {
	s := &Server{}
	tools, err := s.newAgentTools()
	if err != nil {
		t.Fatalf("newAgentTools() error = %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(tools))
	}
	if _, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: tools}); err != nil {
		t.Fatalf("NewToolNode() error = %v", err)
	}
}

func TestAgentToolCallLoop(t *testing.T) {
	testTool, err := toolutils.InferTool("lookup", "测试查询工具", func(_ context.Context, input struct {
		Value string `json:"value"`
	}) (map[string]string, error) {
		return map[string]string{"result": "found:" + input.Value}, nil
	})
	if err != nil {
		t.Fatalf("InferTool() error = %v", err)
	}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}})
	if err != nil {
		t.Fatalf("NewToolNode() error = %v", err)
	}
	calls := 0
	s := &Server{tools: node}
	s.generate = func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{"value":"post"}`}}}}, nil
		}
		if messages[len(messages)-1].Role != schema.Tool || !strings.Contains(messages[len(messages)-1].Content, "found:post") {
			t.Fatalf("tool result was not added to model messages: %#v", messages[len(messages)-1])
		}
		return schema.AssistantMessage("工具结果已回填", nil), nil
	}

	if reply, err := s.runAgentLoop(context.Background(), []*schema.Message{schema.UserMessage("查询")}); err != nil || reply != "工具结果已回填" {
		t.Fatalf("reply = %q, err = %v", reply, err)
	}
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
}

func TestAgentToolCallBudget(t *testing.T) {
	s := &Server{}
	s.generate = func(context.Context, []*schema.Message) (*schema.Message, error) {
		calls := make([]schema.ToolCall, 9)
		for index := range calls {
			calls[index] = schema.ToolCall{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`}}
		}
		return &schema.Message{Role: schema.Assistant, ToolCalls: calls}, nil
	}
	if _, err := s.runAgentLoop(context.Background(), []*schema.Message{schema.UserMessage("查询")}); err == nil || !strings.Contains(err.Error(), "total tool call limit") {
		t.Fatalf("expected total tool call limit error, got %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	development := Config{AppEnv: "development", MySQLDSN: "forum:test@tcp(localhost:3306)/forum"}
	if err := development.Validate(); err != nil {
		t.Fatalf("development config should be accepted: %v", err)
	}
	if err := (Config{AppEnv: "production", MySQLDSN: "forum:test@tcp(mysql:3306)/forum", JWTSecret: "development-secret-change-before-production", AdminRegistrationSecret: "long-enough-admin-secret", CORSOrigins: "https://example.com"}).Validate(); err == nil {
		t.Fatal("production config with default JWT secret should be rejected")
	}
	if err := (Config{AppEnv: "production", MySQLDSN: "forum:strong@tcp(mysql:3306)/forum", JWTSecret: strings.Repeat("x", 32), AdminRegistrationSecret: "long-enough-admin-secret", CORSOrigins: "https://example.com", TrustedProxies: "10.0.0.0/8"}).Validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
	for name, cfg := range map[string]Config{
		"missing MYSQL_DSN":   {AppEnv: "development"},
		"invalid CORS scheme": {AppEnv: "development", MySQLDSN: development.MySQLDSN, CORSOrigins: "javascript:alert(1)"},
		"CORS query":          {AppEnv: "development", MySQLDSN: development.MySQLDSN, CORSOrigins: "https://example.com?debug=1"},
		"trusted proxy":       {AppEnv: "development", MySQLDSN: development.MySQLDSN, TrustedProxies: "not-a-network"},
		"negative log size":   {AppEnv: "development", MySQLDSN: development.MySQLDSN, LogMaxSizeMB: -1},
		"invalid hot gravity": {AppEnv: "development", MySQLDSN: development.MySQLDSN, HotGravity: -1},
		"invalid LLM URL":     {AppEnv: "development", MySQLDSN: development.MySQLDSN, LLMBaseURL: "file:///tmp/model"},
	} {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
}

func TestLoadConfigRejectsInvalidDuration(t *testing.T) {
	t.Setenv("HOT_BASE_HALF_LIFE", "72hours")
	cfg := LoadConfig()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "HOT_BASE_HALF_LIFE") {
		t.Fatalf("invalid duration should be rejected, got %v", err)
	}
}

func TestSelectAgentHistoryAlwaysKeepsNewestMessage(t *testing.T) {
	history := []AgentMessage{
		{ID: 3, Role: "user", Content: strings.Repeat("新", 1200)},
		{ID: 2, Role: "assistant", Content: "旧回答"},
		{ID: 1, Role: "user", Content: "旧问题"},
	}
	selected := selectAgentHistory(history, 1000)
	if len(selected) != 1 || selected[0].ID != 3 || len([]rune(selected[0].Content)) != 1000 {
		t.Fatalf("selected history = %#v", selected)
	}
}

func TestRegisterAdminRequiresSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"username":"20260001","name":"管理员","password":"password123","role":"admin"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Server{cfg: Config{AppEnv: "production", AdminRegistrationSecret: "secret"}}).register(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestAdminRegistrationProtectionMode(t *testing.T) {
	if (&Server{cfg: Config{AppEnv: "development"}}).protectsAdminRegistration() {
		t.Fatal("development without a configured secret should preserve the Apifox request format")
	}
	if !(&Server{cfg: Config{AppEnv: "production"}}).protectsAdminRegistration() {
		t.Fatal("production must protect administrator registration")
	}
	if (&Server{cfg: Config{AppEnv: "development", AdminRegistrationSecret: "configured"}}).protectsAdminRegistration() {
		t.Fatal("development must preserve the original Apifox request format")
	}
}

func TestValidationHelpers(t *testing.T) {
	if !validContent("有效内容", 10) || validContent("   ", 10) || validContent("超过", 1) {
		t.Fatal("validContent returned an unexpected result")
	}
	id1, err1 := newDraftID()
	id2, err2 := newDraftID()
	if err1 != nil || err2 != nil || !strings.HasPrefix(id1, "draft_") || id1 == id2 {
		t.Fatalf("invalid draft IDs: %q %q, errors: %v %v", id1, id2, err1, err2)
	}
	if !validRequestID("request-01:abc") || validRequestID("bad\nrequest") || validRequestID(strings.Repeat("x", 129)) {
		t.Fatal("validRequestID returned an unexpected result")
	}
}

func TestDraftIntentDetection(t *testing.T) {
	for _, message := range []string{"帮我创建一篇招新公告", "请起草一个帖子", "帮我写一条通知"} {
		if !isDraftRequest(message) {
			t.Fatalf("expected draft intent for %q", message)
		}
	}
	for _, message := range []string{"查询最新帖子", "有哪些招新公告", "看看评论", "不要创建招新公告", "如何创建帖子"} {
		if isDraftRequest(message) {
			t.Fatalf("unexpected draft intent for %q", message)
		}
	}
}

func TestRateLimiterWindow(t *testing.T) {
	limiter := newRateLimiter()
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	allowed1, _ := limiter.allow("key", 2, time.Minute, now)
	allowed2, _ := limiter.allow("key", 2, time.Minute, now)
	if !allowed1 || !allowed2 {
		t.Fatal("requests inside the limit should pass")
	}
	if allowed, retryAfter := limiter.allow("key", 2, time.Minute, now); allowed || retryAfter != time.Minute {
		t.Fatal("request above the limit should be rejected")
	}
	if allowed, _ := limiter.allow("key", 2, time.Minute, now.Add(time.Minute)); !allowed {
		t.Fatal("new window should reset the limit")
	}
}

func TestListPostsRejectsDeepOffset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/posts?page=102&page_size=100&sort=latest", nil)
	(&Server{}).listPosts(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestGetPostRejectsInvalidCommentPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "post_id", Value: "1"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/posts/1?comments_limit=101", nil)
	(&Server{}).getPost(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAuthenticateReportsDatabaseFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "test:test@tcp(127.0.0.1:1)/test?timeout=10ms", SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("s", 32)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{TokenVersion: 1, RegisteredClaims: jwt.RegisteredClaims{Subject: "1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	server := &Server{cfg: Config{JWTSecret: secret}, db: db}
	router.GET("/protected", server.authenticate(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+signed)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAgentDraftRequiresGenerator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", nil)
	(&Server{}).createAgentDraft(ctx, 1, "session", "帮我创建一篇公告")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRotatingLogger(t *testing.T) {
	directory := t.TempDir()
	logger, closer, err := NewLogger(Config{LogDir: directory, LogMaxSizeMB: 1, LogMaxBackups: 2, LogMaxAgeDays: 1})
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	payload := strings.Repeat("x", 600<<10)
	logger.Info("large entry one", "payload", payload)
	logger.Info("large entry two", "payload", payload)
	if err := closer.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "app.log.1")); err != nil {
		t.Fatalf("rotated backup missing: %v", err)
	}
}
