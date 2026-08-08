package app

import "time"

type User struct {
	ID           uint64 `gorm:"primaryKey"`
	Username     string `gorm:"size:32;uniqueIndex;not null"`
	Name         string `gorm:"size:32;not null"`
	PasswordHash string `gorm:"size:255;not null"`
	Role         string `gorm:"size:16;not null;index"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Post struct {
	ID        uint64    `gorm:"primaryKey"`
	UserID    uint64    `gorm:"not null;index"`
	Content   string    `gorm:"type:text;not null"`
	Author    User      `gorm:"foreignKey:UserID"`
	CreatedAt time.Time `gorm:"index"`
	UpdatedAt time.Time
}

type Comment struct {
	ID        uint64    `gorm:"primaryKey"`
	PostID    uint64    `gorm:"not null;index:idx_comments_post_time"`
	UserID    uint64    `gorm:"not null;index"`
	Content   string    `gorm:"type:varchar(1000);not null"`
	Author    User      `gorm:"foreignKey:UserID"`
	CreatedAt time.Time `gorm:"index:idx_comments_post_time"`
}

type PostLike struct {
	PostID    uint64 `gorm:"primaryKey"`
	UserID    uint64 `gorm:"primaryKey;index"`
	CreatedAt time.Time
}

type AgentMessage struct {
	ID        uint64    `gorm:"primaryKey"`
	UserID    uint64    `gorm:"not null;index:idx_agent_session"`
	SessionID string    `gorm:"size:128;not null;index:idx_agent_session"`
	Role      string    `gorm:"size:16;not null"`
	Content   string    `gorm:"type:text;not null"`
	CreatedAt time.Time `gorm:"index"`
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
}
