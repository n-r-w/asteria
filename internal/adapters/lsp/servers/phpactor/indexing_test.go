package lspphpactor

import (
	"encoding/json"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexingTestRoot is the workspace root of the indexing state tests, which start no language server.
const indexingTestRoot = "/workspace"

// waitResult holds what waitForIndexing returned.
type waitResult struct {
	indexingReported bool
	err              error
}

// TestWaitForIndexingWithoutProgressEndsAtStartTimeout checks that a wait for indexing in a session that begins no
// indexing progress is still pending just before the start timeout passes and returns without reported indexing when
// it passes.
func TestWaitForIndexingWithoutProgressEndsAtStartTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		service := newIndexingService()
		service.startIndexing(indexingTestRoot)
		results := startWaitForIndexing(t, service)

		time.Sleep(phpactorIndexingStartTimeout - time.Millisecond)
		synctest.Wait()
		requirePending(t, results)

		time.Sleep(time.Millisecond)
		synctest.Wait()
		result := requireFinished(t, results)
		require.NoError(t, result.err)
		assert.False(t, result.indexingReported)
	})
}

// TestWaitForIndexingAfterSessionReplacement checks that a wait that began during the indexing of a session follows
// the session that replaces it: the replacement alone does not report indexing, the indexing of the new session
// releases the wait, and without indexing progress the wait returns at the start timeout of the new session.
func TestWaitForIndexingAfterSessionReplacement(t *testing.T) {
	t.Parallel()

	t.Run("indexing of new session", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			service := newIndexingService()
			startSessionIndexing(service, `"first"`)
			results := startWaitForIndexing(t, service)
			synctest.Wait()
			requirePending(t, results)

			service.startIndexing(indexingTestRoot)
			synctest.Wait()
			requirePending(t, results)

			startSessionProgress(service, `"second"`)
			service.recordProgress(indexingTestRoot, progress(`"second"`, progressKindEnd))
			synctest.Wait()
			result := requireFinished(t, results)
			require.NoError(t, result.err)
			assert.True(t, result.indexingReported)
		})
	})

	t.Run("new session without indexing progress", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			service := newIndexingService()
			startSessionIndexing(service, `"first"`)
			results := startWaitForIndexing(t, service)

			time.Sleep(time.Second)
			service.startIndexing(indexingTestRoot)
			time.Sleep(phpactorIndexingStartTimeout - time.Millisecond)
			synctest.Wait()
			requirePending(t, results)

			time.Sleep(time.Millisecond)
			synctest.Wait()
			result := requireFinished(t, results)
			require.NoError(t, result.err)
			assert.False(t, result.indexingReported)
		})
	})
}

// newIndexingService creates a service with only the indexing state, for tests that start no language server.
func newIndexingService() *Service {
	return &Service{
		rt:                    nil,
		ensureConn:            nil,
		std:                   nil,
		withRequestDocument:   nil,
		runPropertyReferences: nil,
		cacheRoot:             "",
		indexingStartTimeout:  phpactorIndexingStartTimeout,
		mu:                    sync.Mutex{},
		indexing:              make(map[string]*indexingState),
	}
}

// startSessionIndexing starts a new session whose indexing progress with token begins.
func startSessionIndexing(service *Service, token string) {
	service.startIndexing(indexingTestRoot)
	startSessionProgress(service, token)
}

// startSessionProgress announces and begins the progress with token in the current session.
func startSessionProgress(service *Service, token string) {
	service.recordProgressCreate(indexingTestRoot, json.RawMessage(token))
	service.recordProgress(indexingTestRoot, progress(token, progressKindBegin))
}

// progress returns the $/progress params of kind with token.
func progress(token string, kind string) progressParams {
	return progressParams{Token: json.RawMessage(token), Value: progressValue{Kind: kind}}
}

// startWaitForIndexing starts waitForIndexing in a goroutine and returns the channel that receives its result.
func startWaitForIndexing(t *testing.T, service *Service) <-chan waitResult {
	t.Helper()

	results := make(chan waitResult, 1)
	go func() {
		reported, err := service.waitForIndexing(t.Context(), indexingTestRoot)
		results <- waitResult{indexingReported: reported, err: err}
	}()

	return results
}

// requirePending fails the test when the wait has returned.
func requirePending(t *testing.T, results <-chan waitResult) {
	t.Helper()

	select {
	case result := <-results:
		require.FailNow(t, "wait for indexing returned early", "%+v", result)
	default:
	}
}

// requireFinished returns the result of the wait and fails the test when the wait has not returned.
func requireFinished(t *testing.T, results <-chan waitResult) waitResult {
	t.Helper()

	select {
	case result := <-results:
		return result
	default:
		require.FailNow(t, "wait for indexing has not returned")

		return waitResult{indexingReported: false, err: nil}
	}
}
