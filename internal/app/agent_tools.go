package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

type searchPostsInput struct {
	Query string `json:"query,omitempty" jsonschema:"description=帖子正文关键词，可留空"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=返回条数，范围 1 到 20"`
}

type postCommentsInput struct {
	PostID uint64 `json:"post_id" jsonschema:"required,description=帖子 ID"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=返回条数，范围 1 到 50"`
}

type agentUserResult struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
}

type agentPostResult struct {
	ID           uint64          `json:"id"`
	Content      string          `json:"content"`
	Author       agentUserResult `json:"author"`
	LikeCount    int64           `json:"like_count"`
	CommentCount int64           `json:"comment_count"`
	CreatedAt    time.Time       `json:"created_at"`
}

type agentCommentResult struct {
	ID        uint64          `json:"id"`
	PostID    uint64          `json:"post_id"`
	Content   string          `json:"content"`
	Author    agentUserResult `json:"author"`
	CreatedAt time.Time       `json:"created_at"`
}

func toAgentUser(user User) agentUserResult {
	return agentUserResult{ID: user.ID, Username: user.Username, Name: user.Name, Role: user.Role}
}

func truncateAgentText(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes]) + "…"
}

func (s *Server) newAgentTools() ([]tool.BaseTool, error) {
	searchPosts, err := toolutils.InferTool("search_posts", "按正文关键词查询论坛帖子；返回帖子、作者、点赞数和评论数。", func(ctx context.Context, input searchPostsInput) ([]agentPostResult, error) {
		limit := input.Limit
		if limit < 1 || limit > 20 {
			limit = 10
		}
		query := s.db.WithContext(ctx).Preload("Author").Order("created_at DESC").Limit(limit)
		if keyword := strings.TrimSpace(input.Query); keyword != "" {
			query = query.Where("MATCH(content) AGAINST (? IN NATURAL LANGUAGE MODE)", keyword)
		}
		var posts []Post
		if err := query.Find(&posts).Error; err != nil {
			return nil, fmt.Errorf("query posts: %w", err)
		}
		likes, comments, err := s.counts(ctx, posts)
		if err != nil {
			return nil, fmt.Errorf("count post engagement: %w", err)
		}
		results := make([]agentPostResult, 0, len(posts))
		for _, post := range posts {
			results = append(results, agentPostResult{ID: post.ID, Content: truncateAgentText(post.Content, 500), Author: toAgentUser(post.Author), LikeCount: likes[post.ID], CommentCount: comments[post.ID], CreatedAt: post.CreatedAt})
		}
		return results, nil
	})
	if err != nil {
		return nil, fmt.Errorf("create search_posts tool: %w", err)
	}

	getComments, err := toolutils.InferTool("get_post_comments", "按帖子 ID 查询评论，按发布时间升序返回。", func(ctx context.Context, input postCommentsInput) ([]agentCommentResult, error) {
		limit := input.Limit
		if limit < 1 || limit > 50 {
			limit = 50
		}
		var postCount int64
		if err := s.db.WithContext(ctx).Model(&Post{}).Where("id = ?", input.PostID).Count(&postCount).Error; err != nil {
			return nil, fmt.Errorf("check post: %w", err)
		}
		if postCount == 0 {
			return nil, fmt.Errorf("post %d not found", input.PostID)
		}
		var comments []Comment
		if err := s.db.WithContext(ctx).Preload("Author").Where("post_id = ?", input.PostID).Order("created_at ASC").Limit(limit).Find(&comments).Error; err != nil {
			return nil, fmt.Errorf("query comments: %w", err)
		}
		results := make([]agentCommentResult, 0, len(comments))
		for _, comment := range comments {
			results = append(results, agentCommentResult{ID: comment.ID, PostID: comment.PostID, Content: truncateAgentText(comment.Content, 300), Author: toAgentUser(comment.Author), CreatedAt: comment.CreatedAt})
		}
		return results, nil
	})
	if err != nil {
		return nil, fmt.Errorf("create get_post_comments tool: %w", err)
	}

	return []tool.BaseTool{searchPosts, getComments}, nil
}
