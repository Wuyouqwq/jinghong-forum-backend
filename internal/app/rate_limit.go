package app

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

var redisRateLimitScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
local ttl = redis.call("TTL", KEYS[1])
return {count, ttl}
`)

type rateWindow struct {
	count   int
	expires time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]rateWindow
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{windows: make(map[string]rateWindow)}
}

func (l *rateLimiter) allow(key string, limit int, interval time.Duration, now time.Time) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.windows[key]
	if !now.Before(window.expires) {
		window = rateWindow{expires: now.Add(interval)}
	}
	if window.count >= limit {
		l.windows[key] = window
		return false, max(window.expires.Sub(now), time.Second)
	}
	window.count++
	l.windows[key] = window
	if len(l.windows) > 10000 {
		for candidate, value := range l.windows {
			if !now.Before(value.expires) {
				delete(l.windows, candidate)
			}
		}
	}
	return true, 0
}

func (s *Server) limitRequests(scope string, limit int, byUser bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := c.ClientIP()
		if byUser {
			identity = fmt.Sprint(userID(c))
		}
		allowed, retryAfter := s.allowRequest(c, scope, identity, limit)
		if !allowed {
			seconds := int64((retryAfter + time.Second - 1) / time.Second)
			c.Header("Retry-After", fmt.Sprint(max(seconds, int64(1))))
			fail(c, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
			return
		}
		c.Next()
	}
}

func (s *Server) allowRequest(ctx context.Context, scope, identity string, limit int) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	now := time.Now()
	if s.redis != nil {
		key := fmt.Sprintf("forum:rate:%s:%s", scope, identity)
		result, err := redisRateLimitScript.Run(ctx, s.redis, []string{key}, 60).Slice()
		if err == nil && len(result) == 2 {
			count, countOK := result[0].(int64)
			ttl, ttlOK := result[1].(int64)
			if countOK && ttlOK {
				if count <= int64(limit) {
					return true, 0
				}
				return false, time.Duration(max(ttl, int64(1))) * time.Second
			}
		}
		if err == nil {
			err = fmt.Errorf("unexpected redis rate limit result")
		}
		s.warnRedis("redis rate limit failed; using local fallback", "error", err, "scope", scope)
	}
	return s.limiter.allow(scope+":"+identity, limit, time.Minute, now)
}
