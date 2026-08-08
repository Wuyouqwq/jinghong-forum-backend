package app

import (
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Port                    string
	MySQLDSN                string
	JWTSecret               string
	JWTExpires              time.Duration
	RedisAddr               string
	RedisPassword           string
	RedisDB                 int
	LLMAPIKey               string
	LLMBaseURL              string
	LLMModel                string
	CORSOrigins             string
	AdminRegistrationSecret string
}

func LoadConfig() Config {
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
	viper.SetDefault("PORT", "8080")
	viper.SetDefault("MYSQL_DSN", "forum:forum@tcp(mysql:3306)/forum?charset=utf8mb4&parseTime=True&loc=Local")
	viper.SetDefault("JWT_SECRET", "change-me-before-production-32-bytes")
	viper.SetDefault("JWT_EXPIRES", "2h")
	viper.SetDefault("REDIS_ADDR", "redis:6379")
	viper.SetDefault("REDIS_DB", 0)
	viper.SetDefault("LLM_MODEL", "gpt-4o-mini")
	viper.SetDefault("CORS_ORIGINS", "*")
	expires, err := time.ParseDuration(viper.GetString("JWT_EXPIRES"))
	if err != nil || expires <= 0 {
		expires = 2 * time.Hour
	}
	return Config{
		Port:                    viper.GetString("PORT"),
		MySQLDSN:                viper.GetString("MYSQL_DSN"),
		JWTSecret:               viper.GetString("JWT_SECRET"),
		JWTExpires:              expires,
		RedisAddr:               viper.GetString("REDIS_ADDR"),
		RedisPassword:           viper.GetString("REDIS_PASSWORD"),
		RedisDB:                 viper.GetInt("REDIS_DB"),
		LLMAPIKey:               viper.GetString("LLM_API_KEY"),
		LLMBaseURL:              viper.GetString("LLM_BASE_URL"),
		LLMModel:                viper.GetString("LLM_MODEL"),
		CORSOrigins:             viper.GetString("CORS_ORIGINS"),
		AdminRegistrationSecret: viper.GetString("ADMIN_REGISTRATION_SECRET"),
	}
}
