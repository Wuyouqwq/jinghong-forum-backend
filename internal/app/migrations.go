package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

type migration struct {
	version uint64
	name    string
	apply   func(*gorm.DB) error
}

func migrateDatabase(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get migration connection pool: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve migration lock connection: %w", err)
	}
	defer conn.Close()
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", "jinghong_forum_schema_migration", 30).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fmt.Errorf("timed out waiting for migration lock")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(releaseCtx, "SELECT RELEASE_LOCK(?)", "jinghong_forum_schema_migration"); err != nil {
			slog.Warn("release migration lock failed", "error", err)
		}
	}()

	if err := db.AutoMigrate(&User{}, &Post{}, &Comment{}, &PostLike{}, &AgentMessage{}, &AgentDraft{}, &SchemaMigration{}); err != nil {
		return err
	}
	migrations := []migration{
		{version: 1, name: "backfill post engagement counters", apply: backfillPostCounters},
		{version: 2, name: "ensure relational constraints and indexes", apply: ensureSchemaIntegrity},
		{version: 3, name: "backfill human hot ranking signals", apply: backfillHotRankingSignals},
	}
	for _, item := range migrations {
		var count int64
		if err := db.Model(&SchemaMigration{}).Where("version = ?", item.version).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		if err := item.apply(db); err != nil {
			return fmt.Errorf("migration %d (%s): %w", item.version, item.name, err)
		}
		if err := db.Create(&SchemaMigration{Version: item.version, Name: item.name, AppliedAt: time.Now()}).Error; err != nil {
			return err
		}
	}
	return nil
}

func backfillPostCounters(db *gorm.DB) error {
	return db.Exec(`UPDATE posts p
		SET like_count = (SELECT COUNT(*) FROM post_likes l WHERE l.post_id = p.id),
			comment_count = (SELECT COUNT(*) FROM comments c WHERE c.post_id = p.id)`).Error
}

func backfillHotRankingSignals(db *gorm.DB) error {
	return db.Exec(`UPDATE posts AS p
		LEFT JOIN (
			SELECT l.post_id, COUNT(*) AS external_like_count, MAX(l.created_at) AS last_like_at
			FROM post_likes AS l
			JOIN posts AS lp ON lp.id = l.post_id
			WHERE l.user_id <> lp.user_id
			GROUP BY l.post_id
		) AS likes ON likes.post_id = p.id
		LEFT JOIN (
			SELECT c.post_id, COUNT(DISTINCT c.user_id) AS external_commenter_count, MAX(c.created_at) AS last_comment_at
			FROM comments AS c
			JOIN posts AS cp ON cp.id = c.post_id
			WHERE c.user_id <> cp.user_id
			GROUP BY c.post_id
		) AS commenters ON commenters.post_id = p.id
		SET p.external_like_count = COALESCE(likes.external_like_count, 0),
			p.external_commenter_count = COALESCE(commenters.external_commenter_count, 0),
			p.last_external_activity_at = CASE
				WHEN likes.last_like_at IS NULL THEN commenters.last_comment_at
				WHEN commenters.last_comment_at IS NULL THEN likes.last_like_at
				ELSE GREATEST(likes.last_like_at, commenters.last_comment_at)
			END`).Error
}

func ensureSchemaIntegrity(db *gorm.DB) error {
	checks := []struct {
		model      any
		constraint string
	}{
		{&Comment{}, "Post"}, {&Comment{}, "Author"}, {&PostLike{}, "Post"}, {&PostLike{}, "User"},
		{&AgentMessage{}, "User"}, {&AgentDraft{}, "User"},
	}
	for _, check := range checks {
		if db.Migrator().HasConstraint(check.model, check.constraint) {
			continue
		}
		if err := db.Migrator().CreateConstraint(check.model, check.constraint); err != nil {
			return err
		}
	}
	return nil
}
