package app

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	AppEnv                  string
	Port                    string
	MySQLDSN                string
	JWTSecret               string
	JWTExpires              time.Duration
	RedisAddr               string
	RedisPassword           string
	RedisDB                 int
	RedisTLS                bool
	LLMAPIKey               string
	LLMBaseURL              string
	LLMModel                string
	CORSOrigins             string
	TrustedProxies          string
	AdminRegistrationSecret string
	AuthRateLimit           int
	APIRateLimit            int
	WriteRateLimit          int
	AgentRateLimit          int
	AgentHistoryMessages    int
	AgentHistoryRunes       int
	AgentMessageRetention   time.Duration
	AgentDraftRetention     time.Duration
	HotGravity              float64
	HotBaseHalfLife         time.Duration
	HotMomentumHalfLife     time.Duration
	HotRecentWindow         time.Duration
	LogLevel                string
	LogDir                  string
	LogMaxSizeMB            int
	LogMaxBackups           int
	LogMaxAgeDays           int
	loadErr                 error
}

func (c Config) withDefaults() Config {
	if c.AppEnv == "" {
		c.AppEnv = "development"
	}
	if c.Port == "" {
		c.Port = "8080"
	}
	if c.JWTExpires <= 0 {
		c.JWTExpires = 2 * time.Hour
	}
	if c.AuthRateLimit == 0 {
		c.AuthRateLimit = 20
	}
	if c.APIRateLimit == 0 {
		c.APIRateLimit = 600
	}
	if c.WriteRateLimit == 0 {
		c.WriteRateLimit = 120
	}
	if c.AgentRateLimit == 0 {
		c.AgentRateLimit = 12
	}
	if c.AgentHistoryMessages == 0 {
		c.AgentHistoryMessages = 24
	}
	if c.AgentHistoryRunes == 0 {
		c.AgentHistoryRunes = 12000
	}
	if c.AgentMessageRetention <= 0 {
		c.AgentMessageRetention = 30 * 24 * time.Hour
	}
	if c.AgentDraftRetention <= 0 {
		c.AgentDraftRetention = 7 * 24 * time.Hour
	}
	if c.HotGravity == 0 {
		c.HotGravity = 1.2
	}
	if c.HotBaseHalfLife == 0 {
		c.HotBaseHalfLife = 72 * time.Hour
	}
	if c.HotMomentumHalfLife == 0 {
		c.HotMomentumHalfLife = 24 * time.Hour
	}
	if c.HotRecentWindow == 0 {
		c.HotRecentWindow = 7 * 24 * time.Hour
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.LogDir == "" {
		c.LogDir = "logs"
	}
	if c.LogMaxSizeMB == 0 {
		c.LogMaxSizeMB = 20
	}
	if c.LogMaxBackups == 0 {
		c.LogMaxBackups = 5
	}
	if c.LogMaxAgeDays == 0 {
		c.LogMaxAgeDays = 30
	}
	return c
}

func LoadConfig() Config {
	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("APP_ENV", "development")
	v.SetDefault("PORT", "8080")
	v.SetDefault("MYSQL_DSN", "forum:forum@tcp(mysql:3306)/forum?charset=utf8mb4&parseTime=True&loc=Local")
	v.SetDefault("JWT_SECRET", "change-me-before-production-32-bytes")
	v.SetDefault("JWT_EXPIRES", "2h")
	v.SetDefault("REDIS_ADDR", "redis:6379")
	v.SetDefault("REDIS_DB", 0)
	v.SetDefault("REDIS_TLS", false)
	v.SetDefault("LLM_MODEL", "gpt-4o-mini")
	v.SetDefault("CORS_ORIGINS", "*")
	v.SetDefault("TRUSTED_PROXIES", "")
	v.SetDefault("AUTH_RATE_LIMIT", 20)
	v.SetDefault("API_RATE_LIMIT", 600)
	v.SetDefault("WRITE_RATE_LIMIT", 120)
	v.SetDefault("AGENT_RATE_LIMIT", 12)
	v.SetDefault("AGENT_HISTORY_MESSAGES", 24)
	v.SetDefault("AGENT_HISTORY_RUNES", 12000)
	v.SetDefault("AGENT_MESSAGE_RETENTION", "720h")
	v.SetDefault("AGENT_DRAFT_RETENTION", "168h")
	v.SetDefault("HOT_GRAVITY", 1.2)
	v.SetDefault("HOT_BASE_HALF_LIFE", "72h")
	v.SetDefault("HOT_MOMENTUM_HALF_LIFE", "24h")
	v.SetDefault("HOT_RECENT_WINDOW", "168h")
	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("LOG_DIR", "logs")
	v.SetDefault("LOG_MAX_SIZE_MB", 20)
	v.SetDefault("LOG_MAX_BACKUPS", 5)
	v.SetDefault("LOG_MAX_AGE_DAYS", 30)
	expires, expiresErr := parsePositiveDuration("JWT_EXPIRES", v.GetString("JWT_EXPIRES"))
	messageRetention, messageRetentionErr := parsePositiveDuration("AGENT_MESSAGE_RETENTION", v.GetString("AGENT_MESSAGE_RETENTION"))
	draftRetention, draftRetentionErr := parsePositiveDuration("AGENT_DRAFT_RETENTION", v.GetString("AGENT_DRAFT_RETENTION"))
	hotBaseHalfLife, hotBaseHalfLifeErr := parsePositiveDuration("HOT_BASE_HALF_LIFE", v.GetString("HOT_BASE_HALF_LIFE"))
	hotMomentumHalfLife, hotMomentumHalfLifeErr := parsePositiveDuration("HOT_MOMENTUM_HALF_LIFE", v.GetString("HOT_MOMENTUM_HALF_LIFE"))
	hotRecentWindow, hotRecentWindowErr := parsePositiveDuration("HOT_RECENT_WINDOW", v.GetString("HOT_RECENT_WINDOW"))
	return Config{
		AppEnv:                  strings.ToLower(strings.TrimSpace(v.GetString("APP_ENV"))),
		Port:                    v.GetString("PORT"),
		MySQLDSN:                v.GetString("MYSQL_DSN"),
		JWTSecret:               v.GetString("JWT_SECRET"),
		JWTExpires:              expires,
		RedisAddr:               v.GetString("REDIS_ADDR"),
		RedisPassword:           v.GetString("REDIS_PASSWORD"),
		RedisDB:                 v.GetInt("REDIS_DB"),
		RedisTLS:                v.GetBool("REDIS_TLS"),
		LLMAPIKey:               v.GetString("LLM_API_KEY"),
		LLMBaseURL:              v.GetString("LLM_BASE_URL"),
		LLMModel:                v.GetString("LLM_MODEL"),
		CORSOrigins:             v.GetString("CORS_ORIGINS"),
		TrustedProxies:          strings.TrimSpace(v.GetString("TRUSTED_PROXIES")),
		AdminRegistrationSecret: v.GetString("ADMIN_REGISTRATION_SECRET"),
		AuthRateLimit:           v.GetInt("AUTH_RATE_LIMIT"),
		APIRateLimit:            v.GetInt("API_RATE_LIMIT"),
		WriteRateLimit:          v.GetInt("WRITE_RATE_LIMIT"),
		AgentRateLimit:          v.GetInt("AGENT_RATE_LIMIT"),
		AgentHistoryMessages:    v.GetInt("AGENT_HISTORY_MESSAGES"),
		AgentHistoryRunes:       v.GetInt("AGENT_HISTORY_RUNES"),
		AgentMessageRetention:   messageRetention,
		AgentDraftRetention:     draftRetention,
		HotGravity:              v.GetFloat64("HOT_GRAVITY"),
		HotBaseHalfLife:         hotBaseHalfLife,
		HotMomentumHalfLife:     hotMomentumHalfLife,
		HotRecentWindow:         hotRecentWindow,
		LogLevel:                strings.ToLower(strings.TrimSpace(v.GetString("LOG_LEVEL"))),
		LogDir:                  strings.TrimSpace(v.GetString("LOG_DIR")),
		LogMaxSizeMB:            v.GetInt("LOG_MAX_SIZE_MB"),
		LogMaxBackups:           v.GetInt("LOG_MAX_BACKUPS"),
		LogMaxAgeDays:           v.GetInt("LOG_MAX_AGE_DAYS"),
		loadErr:                 errors.Join(expiresErr, messageRetentionErr, draftRetentionErr, hotBaseHalfLifeErr, hotMomentumHalfLifeErr, hotRecentWindowErr),
	}
}

func parsePositiveDuration(name, value string) (time.Duration, error) {
	result, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid Go duration: %w", name, err)
	}
	if result <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return result, nil
}

func (c Config) Validate() error {
	c = c.withDefaults()
	if c.loadErr != nil {
		return c.loadErr
	}
	if c.AppEnv == "" {
		c.AppEnv = "development"
	}
	if c.AppEnv != "development" && c.AppEnv != "test" && c.AppEnv != "production" {
		return fmt.Errorf("APP_ENV must be development, test, or production")
	}
	if c.Port != "" {
		port, err := strconv.Atoi(c.Port)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("PORT must be numeric")
		}
	}
	if c.AuthRateLimit < 0 || c.APIRateLimit < 0 || c.WriteRateLimit < 0 || c.AgentRateLimit < 0 {
		return fmt.Errorf("rate limits cannot be negative")
	}
	if (c.AgentHistoryMessages != 0 && (c.AgentHistoryMessages < 1 || c.AgentHistoryMessages > 200)) || (c.AgentHistoryRunes != 0 && (c.AgentHistoryRunes < 1000 || c.AgentHistoryRunes > 100000)) {
		return fmt.Errorf("agent history limits are invalid")
	}
	if math.IsNaN(c.HotGravity) || math.IsInf(c.HotGravity, 0) || c.HotGravity <= 0 || c.HotGravity > 3 {
		return fmt.Errorf("HOT_GRAVITY must be greater than 0 and at most 3")
	}
	if c.HotBaseHalfLife <= 0 || c.HotMomentumHalfLife <= 0 || c.HotRecentWindow <= 0 {
		return fmt.Errorf("hot ranking durations must be positive")
	}
	if strings.TrimSpace(c.MySQLDSN) == "" {
		return fmt.Errorf("MYSQL_DSN is required")
	}
	if c.RedisDB < 0 {
		return fmt.Errorf("REDIS_DB cannot be negative")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
	if c.LogMaxSizeMB < 1 || c.LogMaxBackups < 1 || c.LogMaxAgeDays < 1 {
		return fmt.Errorf("log rotation limits must be positive")
	}
	if err := validateCORSOrigins(c.CORSOrigins); err != nil {
		return err
	}
	if err := validateTrustedProxies(c.TrustedProxies); err != nil {
		return err
	}
	if c.LLMBaseURL != "" {
		if err := validateHTTPURL("LLM_BASE_URL", c.LLMBaseURL); err != nil {
			return err
		}
	}
	if c.AppEnv != "production" {
		return nil
	}
	if len(c.JWTSecret) < 32 || c.JWTSecret == "change-me-before-production-32-bytes" || c.JWTSecret == "development-secret-change-before-production" {
		return fmt.Errorf("production JWT_SECRET must be a non-default value of at least 32 bytes")
	}
	if len(c.AdminRegistrationSecret) < 16 {
		return fmt.Errorf("production ADMIN_REGISTRATION_SECRET must be at least 16 bytes")
	}
	if strings.TrimSpace(c.CORSOrigins) == "*" {
		return fmt.Errorf("production CORS_ORIGINS must explicitly list allowed origins")
	}
	if strings.Contains(c.MySQLDSN, "forum:forum@") {
		return fmt.Errorf("production MYSQL_DSN must not use the default credentials")
	}
	return nil
}

func validateCORSOrigins(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || value == "*" {
		return nil
	}
	for _, raw := range strings.Split(value, ",") {
		origin := strings.TrimSpace(raw)
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("CORS_ORIGINS contains invalid origin %q", origin)
		}
	}
	return nil
}

func validateTrustedProxies(value string) error {
	for _, raw := range strings.Split(value, ",") {
		proxy := strings.TrimSpace(raw)
		if proxy == "" {
			continue
		}
		if net.ParseIP(proxy) == nil {
			if _, _, err := net.ParseCIDR(proxy); err != nil {
				return fmt.Errorf("TRUSTED_PROXIES contains invalid IP or CIDR %q", proxy)
			}
		}
	}
	return nil
}

func validateHTTPURL(name, value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", name)
	}
	return nil
}
