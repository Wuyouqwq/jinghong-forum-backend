package app

import "time"

type User struct {
	ID           uint64 `gorm:"primaryKey"`
	Username     string `gorm:"size:32;uniqueIndex;not null"`
	Name         string `gorm:"size:32;not null"`
	PasswordHash string `gorm:"size:255;not null"`
	Role         string `gorm:"size:16;not null;index"`
	TokenVersion uint64 `gorm:"not null;default:1"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Post struct {
	ID                     uint64     `gorm:"primaryKey;index:idx_posts_latest,priority:2"`
	UserID                 uint64     `gorm:"not null;index"`
	Content                string     `gorm:"type:text;not null;index:idx_posts_content_fulltext,class:FULLTEXT,option:WITH PARSER ngram"`
	Author                 User       `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT"`
	LikeCount              int64      `gorm:"not null;default:0"`
	CommentCount           int64      `gorm:"not null;default:0"`
	ExternalLikeCount      int64      `gorm:"not null;default:0"`
	ExternalCommenterCount int64      `gorm:"not null;default:0"`
	LastExternalActivityAt *time.Time `gorm:"index"`
	CreatedAt              time.Time  `gorm:"index;index:idx_posts_latest,priority:1"`
	UpdatedAt              time.Time
}

type Comment struct {
	ID        uint64    `gorm:"primaryKey"`
	PostID    uint64    `gorm:"not null;index:idx_comments_post_time;index:idx_comments_recent,priority:2;index:idx_comments_participant,priority:1"`
	UserID    uint64    `gorm:"not null;index;index:idx_comments_recent,priority:3;index:idx_comments_participant,priority:2"`
	Content   string    `gorm:"type:varchar(1000);not null"`
	Post      Post      `gorm:"foreignKey:PostID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	Author    User      `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT"`
	CreatedAt time.Time `gorm:"index:idx_comments_post_time;index:idx_comments_recent,priority:1;index:idx_comments_participant,priority:3"`
}

type PostLike struct {
	PostID    uint64    `gorm:"primaryKey;index:idx_post_likes_recent,priority:2"`
	UserID    uint64    `gorm:"primaryKey;index;index:idx_post_likes_recent,priority:3"`
	CreatedAt time.Time `gorm:"index:idx_post_likes_recent,priority:1"`
	Post      Post      `gorm:"foreignKey:PostID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	User      User      `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

type AgentMessage struct {
	ID        uint64    `gorm:"primaryKey;index:idx_agent_history,priority:3"`
	UserID    uint64    `gorm:"not null;index:idx_agent_session,priority:1;index:idx_agent_history,priority:1"`
	SessionID string    `gorm:"size:128;not null;index:idx_agent_session,priority:2;index:idx_agent_history,priority:2"`
	Role      string    `gorm:"size:16;not null"`
	Content   string    `gorm:"type:text;not null"`
	CreatedAt time.Time `gorm:"index"`
	User      User      `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

type AgentDraft struct {
	ID          string    `gorm:"primaryKey;size:64"`
	UserID      uint64    `gorm:"not null;index"`
	SessionID   string    `gorm:"size:128;not null;index"`
	Action      string    `gorm:"size:32;not null"`
	Content     string    `gorm:"type:text;not null"`
	ExpiresAt   time.Time `gorm:"not null;index"`
	ConfirmedAt *time.Time
	CreatedAt   time.Time
	User        User `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

type SchemaMigration struct {
	Version   uint64 `gorm:"primaryKey"`
	Name      string `gorm:"size:128;not null"`
	AppliedAt time.Time
}
