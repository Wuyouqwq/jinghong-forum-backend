package app

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"gorm.io/gorm"
)

type hotRankParameters struct {
	Gravity          float64
	BaseHalfLife     time.Duration
	MomentumHalfLife time.Duration
	RecentWindow     time.Duration
}

func (c Config) hotRankParameters() hotRankParameters {
	c = c.withDefaults()
	return hotRankParameters{
		Gravity:          c.HotGravity,
		BaseHalfLife:     c.HotBaseHalfLife,
		MomentumHalfLife: c.HotMomentumHalfLife,
		RecentWindow:     c.HotRecentWindow,
	}
}

// hotDecayWeight is a power-law decay normalized so weight(halfLife) == 0.5.
func hotDecayWeight(age, halfLife time.Duration, gravity float64) float64 {
	if age < 0 {
		age = 0
	}
	k := 2 * (math.Pow(2, 1/gravity) - 1)
	return math.Pow(2/(2+k*float64(age)/float64(halfLife)), gravity)
}

func hotRankingScore(params hotRankParameters, age time.Duration, likes, commenters int, recentInteractionAges []time.Duration) float64 {
	quality := math.Log(3 + float64(likes+commenters))
	base := quality * hotDecayWeight(age, params.BaseHalfLife, params.Gravity)
	recentWeight := 0.0
	for _, interactionAge := range recentInteractionAges {
		recentWeight += hotDecayWeight(interactionAge, params.MomentumHalfLife, params.Gravity)
	}
	return base + math.Log1p(recentWeight)
}

func (s *Server) hotPostIDs(tx *gorm.DB, rankingAt time.Time, limit, offset int) ([]uint64, error) {
	params := s.cfg.hotRankParameters()
	recentCutoff := rankingAt.Add(-params.RecentWindow)
	gravity := sqlFloat(params.Gravity)
	k := sqlFloat(2 * (math.Pow(2, 1/params.Gravity) - 1))
	baseSeconds := sqlFloat(params.BaseHalfLife.Seconds())
	momentumSeconds := sqlFloat(params.MomentumHalfLife.Seconds())

	query := fmt.Sprintf(`
SELECT posts.id
FROM posts
LEFT JOIN (
	SELECT post_likes.post_id,
		SUM(POW(
			2 / (2 + %s * GREATEST(TIMESTAMPDIFF(SECOND, post_likes.created_at, ?), 0) / %s),
			%s
		)) AS recent_like_weight
	FROM post_likes FORCE INDEX (idx_post_likes_recent)
	STRAIGHT_JOIN posts AS recent_liked_posts ON recent_liked_posts.id = post_likes.post_id
	WHERE post_likes.user_id <> recent_liked_posts.user_id
		AND post_likes.created_at >= ?
	GROUP BY post_likes.post_id
) AS recent_hot_likes ON recent_hot_likes.post_id = posts.id
LEFT JOIN (
	SELECT recent_commenters.post_id,
		SUM(POW(
			2 / (2 + %s * GREATEST(TIMESTAMPDIFF(SECOND, recent_commenters.last_commented_at, ?), 0) / %s),
			%s
		)) AS recent_commenter_weight
	FROM (
		SELECT comments.post_id, comments.user_id, MAX(comments.created_at) AS last_commented_at
		FROM comments FORCE INDEX (idx_comments_recent)
		STRAIGHT_JOIN posts AS recent_commented_posts ON recent_commented_posts.id = comments.post_id
		WHERE comments.user_id <> recent_commented_posts.user_id
			AND comments.created_at >= ?
		GROUP BY comments.post_id, comments.user_id
	) AS recent_commenters
	GROUP BY recent_commenters.post_id
) AS recent_hot_commenters ON recent_hot_commenters.post_id = posts.id
ORDER BY (
	LN(3 + posts.external_like_count + posts.external_commenter_count)
	* POW(
		2 / (2 + %s * GREATEST(TIMESTAMPDIFF(SECOND, posts.created_at, ?), 0) / %s),
		%s
	)
	+ LN(1 + COALESCE(recent_hot_likes.recent_like_weight, 0) + COALESCE(recent_hot_commenters.recent_commenter_weight, 0))
) DESC,
COALESCE(posts.last_external_activity_at, posts.created_at) DESC,
posts.created_at DESC,
posts.id DESC
LIMIT ? OFFSET ?`,
		k, momentumSeconds, gravity,
		k, momentumSeconds, gravity,
		k, baseSeconds, gravity,
	)

	var ids []uint64
	if err := tx.Raw(query, rankingAt, recentCutoff, rankingAt, recentCutoff, rankingAt, limit, offset).Scan(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func loadPostsInOrder(tx *gorm.DB, ids []uint64) ([]Post, error) {
	if len(ids) == 0 {
		return []Post{}, nil
	}
	var loaded []Post
	if err := tx.Preload("Author").Where("id IN ?", ids).Find(&loaded).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint64]Post, len(loaded))
	for _, post := range loaded {
		byID[post.ID] = post
	}
	ordered := make([]Post, 0, len(ids))
	for _, id := range ids {
		if post, ok := byID[id]; ok {
			ordered = append(ordered, post)
		}
	}
	return ordered, nil
}

func sqlFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 12, 64)
}
