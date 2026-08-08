package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/gin-gonic/gin"
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

func TestRegisterAdminRequiresSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"username":"20260001","name":"管理员","password":"password123","role":"admin"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Server{cfg: Config{AdminRegistrationSecret: "secret"}}).register(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestValidationHelpers(t *testing.T) {
	if !validContent("有效内容", 10) || validContent("   ", 10) || validContent("超过", 1) {
		t.Fatal("validContent returned an unexpected result")
	}
	id1, id2 := newDraftID(), newDraftID()
	if !strings.HasPrefix(id1, "draft_") || id1 == id2 {
		t.Fatalf("invalid draft IDs: %q %q", id1, id2)
	}
}
