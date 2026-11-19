// Package service 承载 M1b-2 的业务用例（认证、书源管理、规则调试、多源搜索）。
// 遵循 Handler -> Service -> Repository/Engine 分层：本包不感知 HTTP。
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
	"golang.org/x/sync/semaphore"
	"yreader/backend/internal/repository"
)

// Auth 相关哨兵错误。HTTP 层统一映射为 401 unauthorized。
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrUnauthorized       = errors.New("auth: unauthorized")
	ErrSessionExpired     = errors.New("auth: session expired")
)

// 密码哈希参数（严格锁定，见 architecture.md 安全规范）。
const (
	argonMemory      = 64 * 1024 // 64 MiB
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLength  = 16
	argonKeyLength   = 32
)

// User 是认证与用户用例的最小模型（避免 service 反向依赖 api）。
type User struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
	Role         string
	DisplayName  *string
	TokenVersion int
	IsDisabled   bool
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// TokenPair 是登录/刷新成功后的令牌对。
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	User         *User
}

const (
	DefaultAccessTTL         = 15 * time.Minute
	maxConcurrentArgonVerify = 4
)

// AuthService 实现登录、刷新轮换、登出与 access token 校验。
type AuthService struct {
	pool       *pgxpool.Pool
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	verifySem  *semaphore.Weighted
}

// NewAuthService 构造认证服务。
func NewAuthService(pool *pgxpool.Pool, secret []byte, accessTTL, refreshTTL time.Duration) *AuthService {
	if len(secret) == 0 {
		panic("auth: JWT secret is required")
	}
	if strings.TrimSpace(os.Getenv("ENVIRONMENT")) != "development" && len(secret) < 32 {
		panic("auth: JWT secret must contain at least 32 bytes unless ENVIRONMENT=development")
	}
	if accessTTL <= 0 {
		accessTTL = DefaultAccessTTL
	}
	if refreshTTL <= 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	return &AuthService{
		pool:       pool,
		secret:     append([]byte(nil), secret...),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		verifySem:  semaphore.NewWeighted(maxConcurrentArgonVerify),
	}
}

// HashPassword 生成 argon2id PHC 字符串（m=65536,t=3,p=2，16 字节盐，32 字节 key）。
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验明文密码与 PHC 字符串是否匹配（恒定时间比较）。
func VerifyPassword(encoded, password string) (bool, error) {
	if len(encoded) > 512 {
		return false, errors.New("auth: password hash too long")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("auth: invalid password hash format")
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, fmt.Errorf("auth: parse password hash params: %w", err)
	}
	if parts[2] != "v=19" || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, iterations, parallelism) || parallelism < 1 || parallelism > 4 || iterations < 1 || iterations > 6 || memory < 8*uint32(parallelism) || memory > 128*1024 {
		return false, errors.New("auth: unsupported password hash parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("auth: decode salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("auth: decode key: %w", err)
	}
	if len(salt) < 8 || len(salt) > 64 || len(want) < 16 || len(want) > 64 {
		return false, errors.New("auth: invalid password hash dimensions")
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// Public dummy PHC: no account or secret is associated with these bytes.
// Every absent-user login still performs one full default-cost verification;
// avoid a second Argon2 calculation and entropy-dependent fallback on first use.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$AAECAwQFBgcICQoLDA0ODw$AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

// Login 校验用户名密码并签发 token 对；任何失败统一 ErrInvalidCredentials。
func (a *AuthService) Login(ctx context.Context, username, password string) (*TokenPair, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		if _, err := a.dummyVerify(ctx, password); err != nil {
			return nil, err
		}
		return nil, ErrInvalidCredentials
	}
	u, err := a.loadByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if _, derr := a.dummyVerify(ctx, password); derr != nil {
				return nil, derr
			}
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	ok, verr := a.verifyPasswordBounded(ctx, u.PasswordHash, password)
	if verr != nil {
		if errors.Is(verr, context.Canceled) || errors.Is(verr, context.DeadlineExceeded) {
			return nil, verr
		}
		return nil, ErrInvalidCredentials
	}
	if !ok || u.IsDisabled {
		return nil, ErrInvalidCredentials
	}
	if _, err := a.pool.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, u.ID); err != nil {
		return nil, err
	}
	return a.issueTokens(ctx, u)
}

func (a *AuthService) verifyPasswordBounded(ctx context.Context, encoded, password string) (bool, error) {
	if a.verifySem != nil {
		if err := a.verifySem.Acquire(ctx, 1); err != nil {
			return false, err
		}
		defer a.verifySem.Release(1)
	}
	return VerifyPassword(encoded, password)
}

func (a *AuthService) dummyVerify(ctx context.Context, password string) (bool, error) {
	ok, err := a.verifyPasswordBounded(ctx, dummyHash, password)
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return false, err
	}
	return ok, nil
}

// Refresh 校验 refresh token 并轮换；命中已吊销 token 视为复用攻击，
// 吊销该用户全部活跃 session 并返回 401。
func (a *AuthService) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, ErrUnauthorized
	}
	hash := hashToken(refreshToken)
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// Serialize the whole rotation/replay decision and replacement insert per user.
	var owner uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT user_id FROM auth_sessions WHERE refresh_token_hash=$1`, hash).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}

	var id, userID uuid.UUID
	var expiresAt time.Time
	var revokedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT id, user_id, expires_at, revoked_at
		FROM auth_sessions WHERE refresh_token_hash = $1`, hash).
		Scan(&id, &userID, &expiresAt, &revokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	if revokedAt != nil {
		// 已吊销 token 被再次使用：吊销该用户全部活跃 session。
		if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrUnauthorized
	}
	if time.Now().After(expiresAt) {
		return nil, ErrSessionExpired
	}

	// 轮换：先吊销旧 session，再签发新 token 对。
	tag, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = now()
		WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrUnauthorized
	}
	u, err := loadAuthUser(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if u.IsDisabled {
		return nil, ErrUnauthorized
	}
	pair, err := a.issueTokensOn(ctx, tx, u)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return pair, nil
}

// Logout 吊销当前 refresh token（幂等）。
func (a *AuthService) Logout(ctx context.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil
	}
	_, err := a.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = now()
		WHERE refresh_token_hash = $1 AND revoked_at IS NULL`, hashToken(refreshToken))
	return err
}

// Me 返回当前用户。
func (a *AuthService) Me(ctx context.Context, userID uuid.UUID) (*User, error) {
	return a.loadByID(ctx, userID)
}

// Authenticate 校验 access token：验签 + exp + token_version 比对。
func (a *AuthService) Authenticate(ctx context.Context, accessToken string) (uuid.UUID, int, error) {
	claims, err := a.parseAccess(accessToken)
	if err != nil {
		return uuid.Nil, 0, err
	}
	var tv int
	if err := a.pool.QueryRow(ctx, `SELECT token_version FROM users WHERE id = $1`, claims.UserID).Scan(&tv); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, 0, ErrUnauthorized
		}
		return uuid.Nil, 0, err
	}
	if tv != claims.TokenVersion {
		return uuid.Nil, 0, ErrUnauthorized
	}
	return claims.UserID, tv, nil
}

// EnsureSeedUser 在用户表为空且提供种子账号时创建管理员用户。
func (a *AuthService) EnsureSeedUser(ctx context.Context, username, password string) (bool, error) {
	var n int
	if err := a.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	_, err = a.pool.Exec(ctx, `INSERT INTO users (username, password_hash, role) VALUES ($1, $2, 'admin')`, username, hash)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *AuthService) loadByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := a.pool.QueryRow(ctx, `SELECT id, username, password_hash, role, display_name,
			token_version, is_disabled, created_at, last_login_at
		FROM users WHERE lower(username) = lower($1)`, username).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName,
		&u.TokenVersion, &u.IsDisabled, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (a *AuthService) loadByID(ctx context.Context, id uuid.UUID) (*User, error) {
	return loadAuthUser(ctx, a.pool, id)
}
func loadAuthUser(ctx context.Context, db repository.Querier, id uuid.UUID) (*User, error) {
	var u User
	err := db.QueryRow(ctx, `SELECT id, username, password_hash, role, display_name,
			token_version, is_disabled, created_at, last_login_at
		FROM users WHERE id = $1`, id).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName,
		&u.TokenVersion, &u.IsDisabled, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	return &u, nil
}

func (a *AuthService) issueTokens(ctx context.Context, u *User) (*TokenPair, error) {
	return a.issueTokensOn(ctx, a.pool, u)
}
func (a *AuthService) issueTokensOn(ctx context.Context, db repository.Querier, u *User) (*TokenPair, error) {
	access, err := a.issueAccess(u.ID, u.TokenVersion)
	if err != nil {
		return nil, err
	}
	plain, hash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ctx, `INSERT INTO auth_sessions (user_id, refresh_token_hash, expires_at)
		VALUES ($1, $2, $3)`, u.ID, hash, time.Now().Add(a.refreshTTL)); err != nil {
		return nil, err
	}
	return &TokenPair{
		AccessToken:  access,
		RefreshToken: plain,
		ExpiresIn:    int(a.accessTTL.Seconds()),
		User:         u,
	}, nil
}

type accessClaims struct {
	jwt.RegisteredClaims
	TokenVersion int `json:"tv"`

	// UserID 由 Subject 解析而来，不参与 JSON 序列化。
	UserID uuid.UUID `json:"-"`
}

func (a *AuthService) issueAccess(userID uuid.UUID, tokenVersion int) (string, error) {
	now := time.Now()
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(a.accessTTL)),
		},
		TokenVersion: tokenVersion,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.secret)
}

func (a *AuthService) parseAccess(token string) (*accessClaims, error) {
	var claims accessClaims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, ErrUnauthorized
		}
		return a.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return nil, ErrUnauthorized
	}
	if claims.Subject == "" {
		return nil, ErrUnauthorized
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, ErrUnauthorized
	}
	claims.UserID = id
	return &claims, nil
}

func newRefreshToken() (plain, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	plain = hex.EncodeToString(b)
	return plain, hashToken(plain), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
