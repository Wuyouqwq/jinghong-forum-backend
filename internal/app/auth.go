package app

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var usernamePattern = regexp.MustCompile(`^[0-9]{1,32}$`)

func (s *Server) authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(c, http.StatusUnauthorized, "未登录或令牌无效")
			return
		}
		token, err := jwt.ParseWithClaims(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")), &claims{}, func(token *jwt.Token) (any, error) {
			if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(s.cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			fail(c, http.StatusUnauthorized, "未登录或令牌无效")
			return
		}
		parsedClaims, valid := token.Claims.(*claims)
		id, parseErr := strconv.ParseUint(parsedClaims.Subject, 10, 64)
		if !valid || parseErr != nil || id == 0 {
			fail(c, http.StatusUnauthorized, "未登录或令牌无效")
			return
		}
		var user User
		err = s.db.WithContext(c).Select("id", "role", "token_version").First(&user, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusUnauthorized, "未登录或令牌无效")
			return
		}
		if err != nil {
			failInternal(c, "authenticate user", err)
			return
		}
		if user.TokenVersion != parsedClaims.TokenVersion {
			fail(c, http.StatusUnauthorized, "未登录或令牌无效")
			return
		}
		c.Set("user_id", id)
		c.Set("role", user.Role)
		c.Next()
	}
}

func (s *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("role") != "admin" {
			fail(c, http.StatusForbidden, "仅管理员可删除任意帖子")
			return
		}
		c.Next()
	}
}

func publicUser(user User) gin.H {
	return gin.H{"id": user.ID, "username": user.Username, "name": user.Name, "role": user.Role}
}

func (s *Server) protectsAdminRegistration() bool { return s.cfg.AppEnv == "production" }

func (s *Server) register(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !bindJSON(c, &request) {
		return
	}
	passwordRunes := utf8.RuneCountInString(request.Password)
	if !usernamePattern.MatchString(request.Username) || !validContent(request.Name, 32) || passwordRunes < 8 || passwordRunes > 16 || (request.Role != "student" && request.Role != "admin") {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return
	}
	if request.Role == "admin" && s.protectsAdminRegistration() {
		provided, expected := c.GetHeader("X-Admin-Registration-Secret"), s.cfg.AdminRegistrationSecret
		if expected == "" || len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			fail(c, http.StatusForbidden, "禁止创建管理员账户")
			return
		}
	}
	var count int64
	if err := s.db.Model(&User{}).Where("username = ?", request.Username).Count(&count).Error; err != nil {
		failInternal(c, "check username", err)
		return
	}
	if count > 0 {
		fail(c, http.StatusConflict, "用户名已存在")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		failInternal(c, "hash password", err)
		return
	}
	user := User{Username: request.Username, Name: request.Name, PasswordHash: string(hash), Role: request.Role}
	if err := s.db.Create(&user).Error; err != nil {
		if isDuplicateKey(err) {
			fail(c, http.StatusConflict, "用户名已存在")
			return
		}
		failInternal(c, "create user", err)
		return
	}
	ok(c, http.StatusCreated, publicUser(user))
}

func (s *Server) login(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &request) {
		return
	}
	usernameRunes := utf8.RuneCountInString(request.Username)
	if usernameRunes < 1 || usernameRunes > 32 || len(request.Password) < 1 || len(request.Password) > 72 {
		fail(c, http.StatusBadRequest, "参数校验失败")
		return
	}
	var user User
	if err := s.db.Where("username = ?", request.Username).First(&user).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusUnauthorized, "账号或密码错误")
		return
	} else if err != nil {
		failInternal(c, "load login user", err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)) != nil {
		fail(c, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{Role: user.Role, TokenVersion: user.TokenVersion, RegisteredClaims: jwt.RegisteredClaims{Subject: strconv.FormatUint(user.ID, 10), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.JWTExpires))}})
	signed, err := token.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		failInternal(c, "sign jwt", err)
		return
	}
	ok(c, http.StatusOK, gin.H{"access_token": signed, "token_type": "Bearer", "expires_in": int64(s.cfg.JWTExpires.Seconds()), "user": publicUser(user)})
}
