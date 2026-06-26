package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrNoRows = fmt.Errorf("no rows in result set")

type Account struct {
	ID                   int64     `json:"id"`
	Name                 string    `json:"name"`
	AccountType          string    `json:"account_type"`
	NSFWEnabled          bool      `json:"nsfw_enabled"`
	SessionID            string    `json:"session_id"`
	ClientCookie         string    `json:"client_cookie"`
	RefreshToken         string    `json:"refresh_token,omitempty"`
	DeviceID             string    `json:"device_id,omitempty"`
	RequestID            string    `json:"request_id,omitempty"`
	SessionCookie        string    `json:"session_cookie"`
	ClientUat            string    `json:"client_uat"`
	ProjectID            string    `json:"project_id"`
	UserID               string    `json:"user_id"`
	AgentMode            string    `json:"agent_mode"`
	Email                string    `json:"email"`
	Weight               int       `json:"weight"`
	Enabled              bool      `json:"enabled"`
	Token                string    `json:"token"`        // Truncated display token
	Subscription         string    `json:"subscription"` // "free", "pro", etc.
	UsageCurrent         float64   `json:"usage_current"`
	UsageTotal           float64   `json:"usage_total"` // Used as lifetime usage
	UsageLimit           float64   `json:"usage_limit"` // Daily limit
	WarpMonthlyLimit     float64   `json:"warp_monthly_limit,omitempty"`
	WarpMonthlyRemaining float64   `json:"warp_monthly_remaining,omitempty"`
	WarpBonusRemaining   float64   `json:"warp_bonus_remaining,omitempty"`
	StatusCode           string    `json:"status_code"`
	LastAttempt          time.Time `json:"last_attempt"`
	QuotaResetAt         time.Time `json:"quota_reset_at"`
	RequestCount         int64     `json:"request_count"`
	LastUsedAt           time.Time `json:"last_used_at"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// SyncState compares this account against a snapshot and returns true if key session/auth fields differ.
func (a *Account) SyncState(snapshot *Account) bool {
	if a == nil || snapshot == nil {
		return false
	}
	return a.SessionID != snapshot.SessionID ||
		a.ClientUat != snapshot.ClientUat ||
		a.ProjectID != snapshot.ProjectID ||
		a.UserID != snapshot.UserID ||
		a.Email != snapshot.Email ||
		a.ClientCookie != snapshot.ClientCookie
}

type Settings struct {
	ID    int64  `json:"id"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ApiKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"`
	KeyFull    string     `json:"-"`
	KeyPrefix  string     `json:"key_prefix"`
	KeySuffix  string     `json:"key_suffix"`
	Enabled    bool       `json:"enabled"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Store struct {
	accounts accountStore
	settings settingsStore
	apiKeys  apiKeyStore
	models   modelStore
}

type Options struct {
	StoreMode     string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	RedisPrefix   string
}

type accountStore interface {
	CreateAccount(ctx context.Context, acc *Account) error
	UpdateAccount(ctx context.Context, acc *Account) error
	DeleteAccount(ctx context.Context, id int64) error
	GetAccount(ctx context.Context, id int64) (*Account, error)
	ListAccounts(ctx context.Context) ([]*Account, error)
	GetEnabledAccounts(ctx context.Context) ([]*Account, error)
	IncrementRequestCount(ctx context.Context, id int64) error
	IncrementUsage(ctx context.Context, id int64, usage float64) error
	IncrementAccountStats(ctx context.Context, id int64, usage float64, count int64) error
}

type settingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

type apiKeyStore interface {
	CreateApiKey(ctx context.Context, key *ApiKey) error
	ListApiKeys(ctx context.Context) ([]*ApiKey, error)
	GetApiKeyByHash(ctx context.Context, hash string) (*ApiKey, error)
	UpdateApiKeyEnabled(ctx context.Context, id int64, enabled bool) error
	UpdateApiKeyLastUsed(ctx context.Context, id int64) error
	DeleteApiKey(ctx context.Context, id int64) error
	GetApiKeyByID(ctx context.Context, id int64) (*ApiKey, error)
}

type modelStore interface {
	CreateModel(ctx context.Context, m *Model) error
	UpdateModel(ctx context.Context, m *Model) error
	DeleteModel(ctx context.Context, id string) error
	GetModel(ctx context.Context, id string) (*Model, error)
	ListModels(ctx context.Context) ([]*Model, error)
	GetModelByModelID(ctx context.Context, modelID string) (*Model, error)
	GetModelByChannelAndModelID(ctx context.Context, channel, modelID string) (*Model, error)
}

type redisClientStore interface {
	Client() *redis.Client
}

type closeableStore interface {
	Close() error
}

func New(opts Options) (*Store, error) {
	store := &Store{}
	redisStore, err := newRedisStore(opts.RedisAddr, opts.RedisPassword, opts.RedisDB, opts.RedisPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to init redis store: %w", err)
	}
	store.accounts = redisStore
	store.settings = redisStore
	store.apiKeys = redisStore
	store.models = redisStore
	if err := store.seedModels(); err != nil {
		slog.Warn("failed to seed models in redis", "error", err)
	}
	return store, nil
}

func (s *Store) seedModels() error {
	ctx := context.Background()
	existing, err := s.ListModels(ctx)
	if err == nil && len(existing) > 0 {
		slog.Debug("Model seed skipped; existing model records preserved", "count", len(existing))
		return nil
	}
	if err != nil {
		slog.Warn("failed to inspect existing models before seed", "error", err)
	}

	models := []Model{
		// Puter model
		// The "unprefixed main model" strategy is used here:
		// Refer to the model.json included with the puter2api warehouse. Only the main model without the provider prefix is ​​included.
		// Do not directly expose aggregate source models such as openrouter:/togetherai: to avoid excessive list expansion.
	}

	models = append(models, buildPuterSeedModels()...)

	for _, m := range models {
		if _, err := s.GetModelByChannelAndModelID(ctx, m.Channel, m.ModelID); err == nil {
			continue
		}
		if err := s.CreateModel(ctx, &m); err != nil {
			slog.Warn("Failed to seed model", "model_id", m.ModelID, "error", err)
		} else {
			slog.Debug("Seeded model", "model_id", m.ModelID)
		}
	}

	return nil
}

func (s *Store) Close() error {
	if s.accounts != nil {
		if closer, ok := s.accounts.(closeableStore); ok {
			return closer.Close()
		}
	}
	return nil
}

// RedisClient returns the underlying Redis client, or nil if not using Redis.
func (s *Store) RedisClient() *redis.Client {
	if s.accounts != nil {
		if rs, ok := s.accounts.(redisClientStore); ok {
			return rs.Client()
		}
	}
	return nil
}

// RedisPrefix returns the configured key prefix.
func (s *Store) RedisPrefix() string {
	if s.accounts != nil {
		if rs, ok := s.accounts.(*redisStore); ok {
			return rs.prefix
		}
	}
	return "warp:"
}

func (s *Store) CreateAccount(ctx context.Context, acc *Account) error {
	if s.accounts != nil {
		return s.accounts.CreateAccount(ctx, acc)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) UpdateAccount(ctx context.Context, acc *Account) error {
	if s.accounts != nil {
		return s.accounts.UpdateAccount(ctx, acc)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	if s.accounts != nil {
		return s.accounts.DeleteAccount(ctx, id)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) GetAccount(ctx context.Context, id int64) (*Account, error) {
	if s.accounts != nil {
		return s.accounts.GetAccount(ctx, id)
	}
	return nil, fmt.Errorf("store not configured")
}

func (s *Store) ListAccounts(ctx context.Context) ([]*Account, error) {
	if s.accounts != nil {
		return s.accounts.ListAccounts(ctx)
	}
	return nil, fmt.Errorf("store not configured")
}

func (s *Store) GetEnabledAccounts(ctx context.Context) ([]*Account, error) {
	if s.accounts != nil {
		return s.accounts.GetEnabledAccounts(ctx)
	}
	return nil, fmt.Errorf("store not configured")
}

func (s *Store) IncrementRequestCount(ctx context.Context, id int64) error {
	if s.accounts != nil {
		return s.accounts.IncrementRequestCount(ctx, id)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) IncrementAccountStats(ctx context.Context, id int64, usage float64, count int64) error {
	if s.accounts != nil {
		return s.accounts.IncrementAccountStats(ctx, id, usage, count)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	if s.settings != nil {
		return s.settings.GetSetting(ctx, key)
	}
	return "", fmt.Errorf("settings store not configured")
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if s.settings != nil {
		return s.settings.SetSetting(ctx, key, value)
	}
	return fmt.Errorf("settings store not configured")
}

func (s *Store) CreateApiKey(ctx context.Context, key *ApiKey) error {
	if s.apiKeys != nil {
		return s.apiKeys.CreateApiKey(ctx, key)
	}
	return fmt.Errorf("api keys store not configured")
}

func (s *Store) ListApiKeys(ctx context.Context) ([]*ApiKey, error) {
	if s.apiKeys != nil {
		return s.apiKeys.ListApiKeys(ctx)
	}
	return nil, fmt.Errorf("api keys store not configured")
}

func (s *Store) UpdateApiKeyEnabled(ctx context.Context, id int64, enabled bool) error {
	if s.apiKeys != nil {
		return s.apiKeys.UpdateApiKeyEnabled(ctx, id, enabled)
	}
	return fmt.Errorf("api keys store not configured")
}

func (s *Store) DeleteApiKey(ctx context.Context, id int64) error {
	if s.apiKeys != nil {
		return s.apiKeys.DeleteApiKey(ctx, id)
	}
	return fmt.Errorf("api keys store not configured")
}

func (s *Store) GetApiKeyByID(ctx context.Context, id int64) (*ApiKey, error) {
	if s.apiKeys != nil {
		return s.apiKeys.GetApiKeyByID(ctx, id)
	}
	return nil, fmt.Errorf("api keys store not configured")
}

// Model wrappers

func (s *Store) CreateModel(ctx context.Context, m *Model) error {
	if s.models != nil {
		if m.IsDefault {
			models, err := s.models.ListModels(ctx)
			if err == nil {
				for _, other := range models {
					if other.Channel == m.Channel && other.IsDefault {
						other.IsDefault = false
						if err := s.models.UpdateModel(ctx, other); err != nil {
							slog.Warn("Failed to clear default flag on model", "model_id", other.ModelID, "error", err)
						}
					}
				}
			}
		}
		return s.models.CreateModel(ctx, m)
	}
	return fmt.Errorf("models store not configured")
}

func (s *Store) UpdateModel(ctx context.Context, m *Model) error {
	if s.models != nil {
		if m.IsDefault {
			models, err := s.models.ListModels(ctx)
			if err == nil {
				for _, other := range models {
					if other.Channel == m.Channel && other.ID != m.ID && other.IsDefault {
						other.IsDefault = false
						if err := s.models.UpdateModel(ctx, other); err != nil {
							slog.Warn("Failed to clear default flag on model", "model_id", other.ModelID, "error", err)
						}
					}
				}
			}
		}
		return s.models.UpdateModel(ctx, m)
	}
	return fmt.Errorf("models store not configured")
}

func (s *Store) DeleteModel(ctx context.Context, id string) error {
	if s.models != nil {
		return s.models.DeleteModel(ctx, id)
	}
	return fmt.Errorf("models store not configured")
}

func (s *Store) GetModel(ctx context.Context, id string) (*Model, error) {
	if s.models != nil {
		return s.models.GetModel(ctx, id)
	}
	return nil, fmt.Errorf("models store not configured")
}

func (s *Store) GetModelByModelID(ctx context.Context, modelID string) (*Model, error) {
	if s.models != nil {
		return s.models.GetModelByModelID(ctx, modelID)
	}
	return nil, fmt.Errorf("models store not configured")
}

func (s *Store) GetModelByChannelAndModelID(ctx context.Context, channel, modelID string) (*Model, error) {
	if s.models != nil {
		return s.models.GetModelByChannelAndModelID(ctx, channel, modelID)
	}
	return nil, fmt.Errorf("models store not configured")
}

func (s *Store) ListModels(ctx context.Context) ([]*Model, error) {
	if s.models != nil {
		return s.models.ListModels(ctx)
	}
	return nil, fmt.Errorf("models store not configured")
}
