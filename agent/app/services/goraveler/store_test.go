package goraveler

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goravel/framework/testing/mock"
)

func TestCacheRetryTrackerRecordFailure(t *testing.T) {
	key := attemptsKey("d-1")
	cacheMock := mock.Factory().Cache()

	cacheMock.EXPECT().GetInt(key, 0).Return(0).Once()
	cacheMock.EXPECT().Put(key, 1, attemptsTTL).Return(nil).Once()
	cacheMock.EXPECT().GetInt(key, 0).Return(1).Once()
	cacheMock.EXPECT().Put(key, 2, attemptsTTL).Return(nil).Once()
	cacheMock.EXPECT().GetInt(key, 0).Return(2).Once()
	cacheMock.EXPECT().Put(key, 3, attemptsTTL).Return(nil).Once()

	tracker := newCacheRetryTracker()
	require.False(t, tracker.RecordFailure("d-1"))
	require.False(t, tracker.RecordFailure("d-1"))
	require.True(t, tracker.RecordFailure("d-1"))

	cacheMock.AssertExpectations(t)
}

func TestCacheRetryTrackerKeepsEventWhenPutFails(t *testing.T) {
	key := attemptsKey("d-1")
	factory := mock.Factory()
	factory.Log()
	cacheMock := factory.Cache()
	cacheMock.EXPECT().GetInt(key, 0).Return(0).Once()
	cacheMock.EXPECT().Put(key, 1, attemptsTTL).Return(errors.New("cache down")).Once()

	require.False(t, newCacheRetryTracker().RecordFailure("d-1"))

	cacheMock.AssertExpectations(t)
}

func TestCacheRetryTrackerReset(t *testing.T) {
	key := attemptsKey("d-1")
	cacheMock := mock.Factory().Cache()
	cacheMock.EXPECT().Forget(key).Return(true).Once()

	newCacheRetryTracker().Reset("d-1")

	cacheMock.AssertExpectations(t)
}

func TestCacheRetryTrackerSkipsEmptyDeliveryID(t *testing.T) {
	// No cache mock is registered: the tracker must not touch the cache.
	tracker := newCacheRetryTracker()

	require.False(t, tracker.RecordFailure(""))
	tracker.Reset("")
}

func TestCacheHandleStore(t *testing.T) {
	key := handledKey("d-1")
	cacheMock := mock.Factory().Cache()
	cacheMock.EXPECT().Has(key).Return(true).Once()
	cacheMock.EXPECT().Put(key, true, handledTTL).Return(nil).Once()

	store := newCacheHandleStore()
	require.True(t, store.Has("d-1"))
	store.Mark("d-1")

	cacheMock.AssertExpectations(t)
}

func TestCacheHandleStoreSkipsEmptyKey(t *testing.T) {
	// No cache mock is registered: an empty key must not touch the cache.
	store := newCacheHandleStore()

	require.False(t, store.Has(""))
	store.Mark("")
}

func TestCacheHandleStoreLogsPutError(t *testing.T) {
	key := handledKey("d-1")
	factory := mock.Factory()
	factory.Log()
	cacheMock := factory.Cache()
	cacheMock.EXPECT().Put(key, true, handledTTL).Return(errors.New("cache down")).Once()

	newCacheHandleStore().Mark("d-1")

	cacheMock.AssertExpectations(t)
}
