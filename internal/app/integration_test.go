//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type testEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func TestAPIContractFlow(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not configured")
	}
	parsedDSN, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse TEST_MYSQL_DSN: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(parsedDSN.DBName), "_test") {
		t.Fatalf("refusing to run destructive integration test against non-test database %q", parsedDSN.DBName)
	}
	if strings.EqualFold(parsedDSN.User, "root") {
		t.Fatal("refusing to run destructive integration test with MySQL root user")
	}
	cfg := Config{
		AppEnv: "test", Port: "18080", MySQLDSN: dsn,
		JWTSecret: strings.Repeat("i", 32), JWTExpires: time.Hour,
		RedisAddr: os.Getenv("TEST_REDIS_ADDR"), AuthRateLimit: 1000, WriteRateLimit: 1000, AgentRateLimit: 1000,
	}
	server, err := NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	cleanIntegrationData(t, server.db)
	t.Cleanup(func() { cleanIntegrationData(t, server.db) })
	server.generateDraft = func(context.Context, []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage("技术部暑期招新开始啦，欢迎大家报名！", nil), nil
	}

	register := func(username, name, role string) {
		response := requestJSON(t, server, http.MethodPost, "/api/v1/auth/register", "", map[string]any{"username": username, "name": name, "password": "password123", "role": role})
		if response.Code != http.StatusCreated {
			t.Fatalf("register %s: status=%d body=%s", username, response.Code, response.Body.String())
		}
	}
	login := func(username string) string {
		response := requestJSON(t, server, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": "password123"})
		var envelope struct {
			Data struct {
				AccessToken string `json:"access_token"`
			} `json:"data"`
		}
		decodeResponse(t, response, &envelope)
		if response.Code != http.StatusOK || envelope.Data.AccessToken == "" {
			t.Fatalf("login %s: status=%d body=%s", username, response.Code, response.Body.String())
		}
		return envelope.Data.AccessToken
	}

	register("20260001", "学生一", "student")
	register("20260002", "学生二", "student")
	register("20260099", "管理员", "admin")
	studentToken, otherToken, adminToken := login("20260001"), login("20260002"), login("20260099")
	assertHumanHotRanking(t, server, studentToken)

	created := requestJSON(t, server, http.MethodPost, "/api/v1/posts", studentToken, map[string]any{"content": "接口契约测试帖子"})
	var createdEnvelope struct {
		Data struct {
			ID uint64 `json:"id"`
		} `json:"data"`
	}
	decodeResponse(t, created, &createdEnvelope)
	postID := createdEnvelope.Data.ID
	if created.Code != http.StatusCreated || postID == 0 {
		t.Fatalf("create post: status=%d body=%s", created.Code, created.Body.String())
	}
	var fulltextMatches int64
	if err := server.db.Model(&Post{}).Where("MATCH(content) AGAINST (? IN NATURAL LANGUAGE MODE)", "接口契约").Count(&fulltextMatches).Error; err != nil || fulltextMatches != 1 {
		t.Fatalf("fulltext post search matches=%d error=%v", fulltextMatches, err)
	}

	for _, path := range []string{"/api/v1/posts?page=1&page_size=20&sort=latest", "/api/v1/posts?page=1&page_size=20&sort=hot"} {
		response := requestJSON(t, server, http.MethodGet, path, studentToken, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("list posts %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if response := requestJSON(t, server, http.MethodDelete, fmt.Sprintf("/api/v1/posts/%d", postID), otherToken, nil); response.Code != http.StatusForbidden {
		t.Fatalf("delete another user's post: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, server, http.MethodPost, fmt.Sprintf("/api/v1/posts/%d/like", postID), otherToken, nil); response.Code != http.StatusOK {
		t.Fatalf("toggle like: status=%d body=%s", response.Code, response.Body.String())
	}
	likeStatusesResponse := requestJSON(t, server, http.MethodPost, "/api/v1/posts/likes", otherToken, map[string]any{"post_ids": []uint64{postID}})
	var likeStatusesEnvelope struct {
		Data struct {
			Status []struct {
				PostID uint64 `json:"post_id"`
				Liked  bool   `json:"liked"`
			} `json:"status"`
		} `json:"data"`
	}
	decodeResponse(t, likeStatusesResponse, &likeStatusesEnvelope)
	if likeStatusesResponse.Code != http.StatusOK || len(likeStatusesEnvelope.Data.Status) != 1 || likeStatusesEnvelope.Data.Status[0].PostID != postID || !likeStatusesEnvelope.Data.Status[0].Liked {
		t.Fatalf("like statuses: status=%d body=%s", likeStatusesResponse.Code, likeStatusesResponse.Body.String())
	}
	if response := requestJSON(t, server, http.MethodPost, fmt.Sprintf("/api/v1/posts/%d/comment", postID), otherToken, map[string]any{"content": "测试评论"}); response.Code != http.StatusCreated {
		t.Fatalf("create comment: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, server, http.MethodPost, fmt.Sprintf("/api/v1/posts/%d/comment", postID), otherToken, map[string]any{"content": "第二条测试评论"}); response.Code != http.StatusCreated {
		t.Fatalf("create second comment: status=%d body=%s", response.Code, response.Body.String())
	}
	postResponse := requestJSON(t, server, http.MethodGet, fmt.Sprintf("/api/v1/posts/%d?comments_limit=1", postID), studentToken, nil)
	var postEnvelope struct {
		Data struct {
			LikeCount    int64 `json:"like_count"`
			CommentCount int64 `json:"comment_count"`
			Comments     []struct {
				ID uint64 `json:"id"`
			} `json:"comments"`
			CommentsMeta struct {
				NextCursor uint64 `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
			} `json:"comments_meta"`
		} `json:"data"`
	}
	decodeResponse(t, postResponse, &postEnvelope)
	if postResponse.Code != http.StatusOK || postEnvelope.Data.LikeCount != 1 || postEnvelope.Data.CommentCount != 2 || len(postEnvelope.Data.Comments) != 1 || !postEnvelope.Data.CommentsMeta.HasMore || postEnvelope.Data.CommentsMeta.NextCursor == 0 {
		t.Fatalf("get post counters: status=%d body=%s", postResponse.Code, postResponse.Body.String())
	}
	secondCommentPage := requestJSON(t, server, http.MethodGet, fmt.Sprintf("/api/v1/posts/%d?comments_limit=1&after_comment_id=%d", postID, postEnvelope.Data.CommentsMeta.NextCursor), studentToken, nil)
	var secondPageEnvelope struct {
		Data struct {
			Comments []struct {
				ID uint64 `json:"id"`
			} `json:"comments"`
			CommentsMeta struct {
				HasMore bool `json:"has_more"`
			} `json:"comments_meta"`
		} `json:"data"`
	}
	decodeResponse(t, secondCommentPage, &secondPageEnvelope)
	if secondCommentPage.Code != http.StatusOK || len(secondPageEnvelope.Data.Comments) != 1 || secondPageEnvelope.Data.Comments[0].ID == postEnvelope.Data.Comments[0].ID || secondPageEnvelope.Data.CommentsMeta.HasMore {
		t.Fatalf("second comment page: status=%d body=%s", secondCommentPage.Code, secondCommentPage.Body.String())
	}
	var rankedPost Post
	if err := server.db.First(&rankedPost, postID).Error; err != nil || rankedPost.ExternalLikeCount != 1 || rankedPost.ExternalCommenterCount != 1 || rankedPost.LastExternalActivityAt == nil {
		t.Fatalf("hot ranking signals: likes=%d commenters=%d last=%v error=%v", rankedPost.ExternalLikeCount, rankedPost.ExternalCommenterCount, rankedPost.LastExternalActivityAt, err)
	}
	if response := requestJSON(t, server, http.MethodPost, fmt.Sprintf("/api/v1/posts/%d/like", postID), otherToken, nil); response.Code != http.StatusOK {
		t.Fatalf("remove external like: status=%d body=%s", response.Code, response.Body.String())
	}
	if err := server.db.First(&rankedPost, postID).Error; err != nil || rankedPost.ExternalLikeCount != 0 || rankedPost.ExternalCommenterCount != 1 || rankedPost.LastExternalActivityAt == nil {
		t.Fatalf("signals after unlike: likes=%d commenters=%d last=%v error=%v", rankedPost.ExternalLikeCount, rankedPost.ExternalCommenterCount, rankedPost.LastExternalActivityAt, err)
	}
	if response := requestJSON(t, server, http.MethodPost, fmt.Sprintf("/api/v1/posts/%d/like", postID), otherToken, nil); response.Code != http.StatusOK {
		t.Fatalf("restore external like: status=%d body=%s", response.Code, response.Body.String())
	}

	draftResponse := requestJSON(t, server, http.MethodPost, "/api/v1/agent/chat", studentToken, map[string]any{"session_id": "integration-session", "message": "帮我创建一篇招新公告"})
	var draftEnvelope struct {
		Data struct {
			PendingAction struct {
				DraftID string `json:"draft_id"`
			} `json:"pending_action"`
		} `json:"data"`
	}
	decodeResponse(t, draftResponse, &draftEnvelope)
	if draftResponse.Code != http.StatusOK || draftEnvelope.Data.PendingAction.DraftID == "" {
		t.Fatalf("create draft: status=%d body=%s", draftResponse.Code, draftResponse.Body.String())
	}
	confirmResponse := requestJSON(t, server, http.MethodPost, "/api/v1/agent/chat", studentToken, map[string]any{"session_id": "integration-session", "message": "确认发布", "confirm_draft_id": draftEnvelope.Data.PendingAction.DraftID})
	if confirmResponse.Code != http.StatusOK {
		t.Fatalf("confirm draft: status=%d body=%s", confirmResponse.Code, confirmResponse.Body.String())
	}
	var messageCount int64
	if err := server.db.Model(&AgentMessage{}).Where("session_id = ?", "integration-session").Count(&messageCount).Error; err != nil || messageCount != 4 {
		t.Fatalf("agent message count = %d, error=%v", messageCount, err)
	}

	var agentPost Post
	if err := server.db.Where("content = ?", "技术部暑期招新开始啦，欢迎大家报名！").First(&agentPost).Error; err != nil {
		t.Fatalf("load confirmed agent post: %v", err)
	}
	if response := requestJSON(t, server, http.MethodDelete, fmt.Sprintf("/api/v1/admin/posts/%d", agentPost.ID), adminToken, nil); response.Code != http.StatusOK {
		t.Fatalf("admin delete: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, server, http.MethodDelete, fmt.Sprintf("/api/v1/posts/%d", postID), studentToken, nil); response.Code != http.StatusOK {
		t.Fatalf("owner delete: status=%d body=%s", response.Code, response.Body.String())
	}
	var dependentCount int64
	if err := server.db.Model(&Comment{}).Where("post_id = ?", postID).Count(&dependentCount).Error; err != nil || dependentCount != 0 {
		t.Fatalf("comments were not cascade deleted: %d", dependentCount)
	}
}

func assertHumanHotRanking(t *testing.T, server *Server, token string) {
	t.Helper()
	var author, participant, secondParticipant User
	for username, target := range map[string]*User{
		"20260001": &author,
		"20260002": &participant,
		"20260099": &secondParticipant,
	} {
		if err := server.db.Where("username = ?", username).First(target).Error; err != nil {
			t.Fatalf("load ranking user %s: %v", username, err)
		}
	}

	now := time.Now()
	posts := []*Post{
		{UserID: author.ID, Content: "热门排序-新帖待发现", CreatedAt: now.Add(-time.Hour)},
		{UserID: author.ID, Content: "热门排序-单人重复评论", CreatedAt: now.Add(-12 * time.Hour)},
		{UserID: author.ID, Content: "热门排序-多人参与", CreatedAt: now.Add(-12 * time.Hour)},
		{UserID: author.ID, Content: "热门排序-旧帖复活", CreatedAt: now.Add(-30 * 24 * time.Hour)},
		{UserID: author.ID, Content: "热门排序-作者自顶", CreatedAt: now.Add(-12 * time.Hour)},
	}
	for _, post := range posts {
		if err := server.db.Create(post).Error; err != nil {
			t.Fatalf("create ranking post: %v", err)
		}
	}

	comments := make([]Comment, 0, 16)
	for index := 0; index < 10; index++ {
		comments = append(comments, Comment{PostID: posts[1].ID, UserID: participant.ID, Content: fmt.Sprintf("重复评论%d", index), CreatedAt: now.Add(-time.Duration(index+1) * time.Minute)})
	}
	comments = append(comments,
		Comment{PostID: posts[2].ID, UserID: participant.ID, Content: "参与者一", CreatedAt: now.Add(-time.Hour)},
		Comment{PostID: posts[2].ID, UserID: secondParticipant.ID, Content: "参与者二", CreatedAt: now.Add(-2 * time.Hour)},
		Comment{PostID: posts[3].ID, UserID: participant.ID, Content: "旧帖重新讨论一", CreatedAt: now.Add(-time.Hour)},
		Comment{PostID: posts[3].ID, UserID: secondParticipant.ID, Content: "旧帖重新讨论二", CreatedAt: now.Add(-2 * time.Hour)},
	)
	for index := 0; index < 5; index++ {
		comments = append(comments, Comment{PostID: posts[4].ID, UserID: author.ID, Content: fmt.Sprintf("作者自顶%d", index), CreatedAt: now.Add(-time.Duration(index+1) * time.Minute)})
	}
	if err := server.db.Create(&comments).Error; err != nil {
		t.Fatalf("create ranking comments: %v", err)
	}
	likes := []PostLike{
		{PostID: posts[3].ID, UserID: participant.ID, CreatedAt: now.Add(-time.Hour)},
		{PostID: posts[3].ID, UserID: secondParticipant.ID, CreatedAt: now.Add(-2 * time.Hour)},
		{PostID: posts[4].ID, UserID: author.ID, CreatedAt: now.Add(-time.Minute)},
	}
	if err := server.db.Create(&likes).Error; err != nil {
		t.Fatalf("create ranking likes: %v", err)
	}
	if err := backfillHotRankingSignals(server.db); err != nil {
		t.Fatalf("backfill ranking signals: %v", err)
	}

	response := requestJSON(t, server, http.MethodGet, "/api/v1/posts?page=1&page_size=100&sort=hot", token, nil)
	var envelope struct {
		Data struct {
			Items []struct {
				ID uint64 `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	decodeResponse(t, response, &envelope)
	if response.Code != http.StatusOK {
		t.Fatalf("list human hot ranking: status=%d body=%s", response.Code, response.Body.String())
	}
	positions := make(map[uint64]int, len(envelope.Data.Items))
	for index, item := range envelope.Data.Items {
		positions[item.ID] = index
	}
	assertBefore := func(first, second *Post, reason string) {
		t.Helper()
		firstPosition, firstFound := positions[first.ID]
		secondPosition, secondFound := positions[second.ID]
		if !firstFound || !secondFound || firstPosition >= secondPosition {
			t.Fatalf("%s: positions first=%d/%v second=%d/%v; body=%s", reason, firstPosition, firstFound, secondPosition, secondFound, response.Body.String())
		}
	}
	assertBefore(posts[2], posts[1], "multiple participants should outrank repeated comments from one user")
	assertBefore(posts[3], posts[0], "genuinely revived old post should outrank an unseen new post")
	assertBefore(posts[0], posts[4], "author self-interactions must not raise hot rank")
}

func cleanIntegrationData(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []string{"agent_drafts", "agent_messages", "post_likes", "comments", "posts", "users"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Errorf("clean %s: %v", table, err)
		}
	}
}

func requestJSON(t *testing.T, server *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &payload)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.router.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}
