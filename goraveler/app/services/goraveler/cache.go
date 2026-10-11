package goraveler

import (
	"time"

	"goravel/app/facades"
)

const (
	// attemptsTTL is how long a failure counter is retained.
	attemptsTTL = 24 * time.Hour
	// attemptsKeyPrefix namespaces failure counters.
	attemptsKeyPrefix = "goraveler:attempts:"
	// handledKeyPrefix namespaces successfully handled events.
	handledKeyPrefix = "goraveler:handled:"
	// handledTTL is how long a handled event is remembered.
	handledTTL = 24 * time.Hour
)

// cacheRetryTracker is a retryTracker backed by the application cache.
type cacheRetryTracker struct{}

// newCacheRetryTracker returns a cache-backed retry tracker.
func newCacheRetryTracker() *cacheRetryTracker {
	return &cacheRetryTracker{}
}

func (t *cacheRetryTracker) RecordFailure(key string) bool {
	if key == "" {
		return false
	}

	cacheKey := attemptsKey(key)
	cache := facades.Cache()
	count := cache.GetInt(cacheKey, 0) + 1
	if err := cache.Put(cacheKey, count, attemptsTTL); err != nil {
		// A transient cache outage must not drop an otherwise retryable event.
		facades.Log().Errorf("goraveler: record attempt %q: %v", key, err)
		return false
	}

	return count >= maxAttempts
}

func (t *cacheRetryTracker) Reset(key string) {
	if key == "" {
		return
	}

	facades.Cache().Forget(attemptsKey(key))
}

func attemptsKey(key string) string {
	return attemptsKeyPrefix + key
}

// cacheHandleStore is a handleStore backed by the application cache.
type cacheHandleStore struct{}

// newCacheHandleStore returns a cache-backed handle store.
func newCacheHandleStore() *cacheHandleStore {
	return &cacheHandleStore{}
}

func (s *cacheHandleStore) Has(key string) bool {
	if key == "" {
		return false
	}

	return facades.Cache().Has(handledKey(key))
}

func (s *cacheHandleStore) Mark(key string) {
	if key == "" {
		return
	}

	if err := facades.Cache().Put(handledKey(key), true, handledTTL); err != nil {
		facades.Log().Errorf("goraveler: record handled %q: %v", key, err)
	}
}

func handledKey(key string) string {
	return handledKeyPrefix + key
}
