package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookieName  = "megaapp_session"
	defaultSessionTTL  = 30 * 24 * time.Hour
	defaultRenewWindow = 7 * 24 * time.Hour
)

var (
	ErrUsernameTaken  = errors.New("username is taken")
	ErrInvalidCreds   = errors.New("invalid username and/or password")
	ErrInvalidPayload = errors.New("invalid payload")
	ErrInvalidSession = errors.New("invalid session")
)

type SessionConfig struct {
	TTL         time.Duration
	RenewWindow time.Duration
}

type Identity struct {
	SessionID string
	UserID    int64
	Username  string
	IsAdmin   bool
	ExpiresAt time.Time
}

type LoginResult struct {
	Identity Identity
	Cookie   string
}

type Service struct {
	repo        *Repository
	ttl         time.Duration
	renewWindow time.Duration
	now         func() time.Time
}

func NewService(repo *Repository, config SessionConfig) *Service {
	if config.TTL <= 0 {
		config.TTL = defaultSessionTTL
	}
	if config.RenewWindow <= 0 || config.RenewWindow >= config.TTL {
		config.RenewWindow = defaultRenewWindow
	}
	return &Service{repo: repo, ttl: config.TTL, renewWindow: config.RenewWindow, now: time.Now}
}

func (s *Service) Register(ctx context.Context, username string, password string) (int64, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return 0, ErrInvalidPayload
	}

	existingUser, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		return 0, err
	}
	if existingUser != nil {
		return 0, ErrUsernameTaken
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, fmt.Errorf("hash password: %w", err)
	}

	return s.repo.CreateUser(ctx, username, string(hashedPassword))
}

func (s *Service) Login(ctx context.Context, username string, password string) (LoginResult, error) {
	user, err := s.repo.GetUserByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		return LoginResult{}, err
	}
	if user == nil || bcrypt.CompareHashAndPassword([]byte(user.HashedPassword), []byte(password)) != nil {
		return LoginResult{}, ErrInvalidCreds
	}

	return s.newSession(ctx, user)
}

func (s *Service) AuthenticateCookie(ctx context.Context, cookieValue string) (Identity, error) {
	sessionID, secret, ok := splitCookie(cookieValue)
	if !ok {
		return Identity{}, ErrInvalidSession
	}

	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return Identity{}, err
	}
	if session == nil || session.RevokedAt != nil || !session.ExpiresAt.After(s.now()) || !equalSecretHash(session.SecretHash, secret) {
		return Identity{}, ErrInvalidSession
	}

	user, err := s.repo.GetUserByID(ctx, session.UserID)
	if err != nil {
		return Identity{}, err
	}
	if user == nil {
		return Identity{}, ErrInvalidSession
	}

	return Identity{SessionID: session.ID, UserID: user.ID, Username: user.Username, IsAdmin: user.IsAdmin, ExpiresAt: session.ExpiresAt}, nil
}

func (s *Service) Renew(ctx context.Context, identity Identity) (Identity, error) {
	if identity.ExpiresAt.Sub(s.now()) > s.renewWindow {
		return identity, nil
	}
	now := s.now()
	expiresAt := now.Add(s.ttl)
	if err := s.repo.RenewSession(ctx, identity.SessionID, expiresAt, now); err != nil {
		return Identity{}, err
	}
	identity.ExpiresAt = expiresAt
	return identity, nil
}

func (s *Service) Revoke(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return ErrInvalidSession
	}
	return s.repo.RevokeSession(ctx, sessionID, s.now())
}

func (s *Service) CreateSession(ctx context.Context, userID int64) (LoginResult, error) {
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return LoginResult{}, err
	}
	if user == nil {
		return LoginResult{}, ErrInvalidSession
	}
	return s.newSession(ctx, user)
}

func (s *Service) ListAdminUserIDs(ctx context.Context) ([]int64, error) {
	return s.repo.ListAdminUserIDs(ctx)
}

func (s *Service) GetUserByID(ctx context.Context, userID int64) (*User, error) {
	return s.repo.GetUserByID(ctx, userID)
}

func (s *Service) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		return false, err
	}
	return user.IsAdmin, nil
}

func (s *Service) newSession(ctx context.Context, user *User) (LoginResult, error) {
	id, err := randomValue(24)
	if err != nil {
		return LoginResult{}, err
	}
	secret, err := randomValue(32)
	if err != nil {
		return LoginResult{}, err
	}
	now := s.now()
	expiresAt := now.Add(s.ttl)
	if err := s.repo.DeleteExpiredSessions(ctx, now); err != nil {
		return LoginResult{}, err
	}
	if err := s.repo.CreateSession(ctx, Session{
		ID: id, SecretHash: hashSecret(secret), UserID: user.ID, CreatedAt: now, ExpiresAt: expiresAt, RenewedAt: now,
	}); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		Identity: Identity{SessionID: id, UserID: user.ID, Username: user.Username, IsAdmin: user.IsAdmin, ExpiresAt: expiresAt},
		Cookie:   id + "." + secret,
	}, nil
}

func randomValue(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate session secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func splitCookie(value string) (string, string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func hashSecret(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

func equalSecretHash(actual []byte, secret string) bool {
	expected := hashSecret(secret)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
