package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrRequestInProgress = errors.New("concurrent request with same idempotency key is already in progress")
	ErrInvalidKey        = errors.New("idempotency key cannot be empty")
)

type Status string

const (
	StatusStarted   Status = "STARTED"
	StatusCompleted Status = "COMPLETED"
	StatusFailed    Status = "FAILED"
)

// Record represents cached idempotency execution state.
type Record struct {
	Key          string            `json:"key"`
	Status       Status            `json:"status"`
	StatusCode   int               `json:"status_code"`
	Headers      map[string]string `json:"headers"`
	ResponseBody []byte            `json:"response_body"`
	CreatedAt    time.Time         `json:"created_at"`
}

// Storage defines interface for persisting idempotency state.
type Storage interface {
	// TryLock returns (acquired=true, nil, nil) if lock obtained.
	// Returns (acquired=false, existingRecord, nil) if key already exists.
	TryLock(ctx context.Context, key string, inFlightTTL time.Duration) (bool, *Record, error)
	SaveResponse(ctx context.Context, key string, record *Record, completedTTL time.Duration) error
	ReleaseLock(ctx context.Context, key string) error
}

// RedisStorage implements Storage backed by Redis.
type RedisStorage struct {
	client *redis.Client
}

// NewRedisStorage initializes RedisStorage.
func NewRedisStorage(client *redis.Client) *RedisStorage {
	return &RedisStorage{client: client}
}

func (s *RedisStorage) formatKey(key string) string {
	return fmt.Sprintf("idempotency:%s", key)
}

func (s *RedisStorage) TryLock(ctx context.Context, key string, inFlightTTL time.Duration) (bool, *Record, error) {
	if s.client == nil {
		return true, nil, nil
	}
	rKey := s.formatKey(key)

	initialRecord := Record{
		Key:       key,
		Status:    StatusStarted,
		CreatedAt: time.Now(),
	}
	data, _ := json.Marshal(initialRecord)

	ok, err := s.client.SetNX(ctx, rKey, data, inFlightTTL).Result()
	if err != nil {
		return false, nil, err
	}
	if ok {
		return true, nil, nil
	}

	// Key already exists, fetch existing record
	val, err := s.client.Get(ctx, rKey).Result()
	if err != nil {
		return false, nil, err
	}

	var existing Record
	if err := json.Unmarshal([]byte(val), &existing); err != nil {
		return false, nil, err
	}

	return false, &existing, nil
}

func (s *RedisStorage) SaveResponse(ctx context.Context, key string, record *Record, completedTTL time.Duration) error {
	if s.client == nil {
		return nil
	}
	rKey := s.formatKey(key)
	record.Status = StatusCompleted

	data, err := json.Marshal(record)
	if err != nil {
		return err
	}

	return s.client.Set(ctx, rKey, data, completedTTL).Err()
}

func (s *RedisStorage) ReleaseLock(ctx context.Context, key string) error {
	if s.client == nil {
		return nil
	}
	rKey := s.formatKey(key)
	return s.client.Del(ctx, rKey).Err()
}

// MemoryStorage provides in-memory implementation for testing/single-instance.
type MemoryStorage struct {
	mu      sync.Mutex
	records map[string]*Record
}

// NewMemoryStorage initializes MemoryStorage.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		records: make(map[string]*Record),
	}
}

func (m *MemoryStorage) TryLock(ctx context.Context, key string, inFlightTTL time.Duration) (bool, *Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if rec, exists := m.records[key]; exists {
		return false, rec, nil
	}

	rec := &Record{
		Key:       key,
		Status:    StatusStarted,
		CreatedAt: time.Now(),
	}
	m.records[key] = rec
	return true, nil, nil
}

func (m *MemoryStorage) SaveResponse(ctx context.Context, key string, record *Record, completedTTL time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record.Status = StatusCompleted
	m.records[key] = record
	return nil
}

func (m *MemoryStorage) ReleaseLock(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key)
	return nil
}
