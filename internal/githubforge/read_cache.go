package githubforge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"zooid/internal/forge"
)

const (
	githubReadCacheTTL          = 30 * time.Second
	githubNegativeReadCacheTTL  = 5 * time.Second
	githubReadCacheMaximumBytes = 64 << 20
	githubReadCacheMaximumEntry = 4 << 20
	githubReadCacheMaximumItems = 256
	githubBrowserCacheControl   = "private, max-age=15"
)

type githubReadCacheScope struct {
	connectionID string
	repositoryID int64
}

type githubReadCacheKey struct {
	githubReadCacheScope
	path string
}

type githubReadCacheEntry struct {
	statusCode int
	header     http.Header
	body       []byte
	err        *forge.Error
	expiresAt  time.Time
	sequence   uint64
}

type githubReadCacheCall struct {
	done  chan struct{}
	entry githubReadCacheEntry
	err   error
}

type githubReadCache struct {
	mu          sync.Mutex
	entries     map[githubReadCacheKey]githubReadCacheEntry
	inflight    map[githubReadCacheKey]*githubReadCacheCall
	generations map[githubReadCacheScope]uint64
	bytes       int64
	sequence    uint64
	now         func() time.Time
}

func newGitHubReadCache() *githubReadCache {
	return &githubReadCache{
		entries:     make(map[githubReadCacheKey]githubReadCacheEntry),
		inflight:    make(map[githubReadCacheKey]*githubReadCacheCall),
		generations: make(map[githubReadCacheScope]uint64),
		now:         time.Now,
	}
}

func (cache *githubReadCache) load(ctx context.Context, key githubReadCacheKey, fetch func() (*http.Response, error)) (*http.Response, error) {
	cache.mu.Lock()
	if entry, found := cache.entries[key]; found {
		if entry.expiresAt.After(cache.now()) {
			cache.sequence++
			entry.sequence = cache.sequence
			cache.entries[key] = entry
			cache.mu.Unlock()
			return cachedGitHubResponse(entry)
		}
		cache.removeLocked(key, entry)
	}
	if call, found := cache.inflight[key]; found {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
			if call.err != nil {
				return nil, call.err
			}
			return cachedGitHubResponse(call.entry)
		}
	}
	call := &githubReadCacheCall{done: make(chan struct{})}
	cache.inflight[key] = call
	generation := cache.generations[key.githubReadCacheScope]
	cache.mu.Unlock()

	response, err := fetch()
	entry := githubReadCacheEntry{}
	if err == nil {
		entry, err = readGitHubResponse(response)
	} else if cachedError := cacheableGitHubReadError(err); cachedError != nil {
		entry = githubReadCacheEntry{err: cachedError}
		err = nil
	}

	cache.mu.Lock()
	if err == nil {
		cache.sequence++
		entry.sequence = cache.sequence
		ttl := githubReadCacheTTL
		if entry.err != nil {
			ttl = githubNegativeReadCacheTTL
		}
		entry.expiresAt = cache.now().Add(ttl)
		if cache.generations[key.githubReadCacheScope] == generation && len(entry.body) <= githubReadCacheMaximumEntry {
			cache.putLocked(key, entry)
		}
		call.entry = entry
	} else {
		call.err = err
	}
	delete(cache.inflight, key)
	close(call.done)
	cache.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return cachedGitHubResponse(entry)
}

func (cache *githubReadCache) invalidate(connectionID string, repositoryID int64) {
	scope := githubReadCacheScope{connectionID: connectionID, repositoryID: repositoryID}
	cache.mu.Lock()
	cache.generations[scope]++
	for key, entry := range cache.entries {
		if key.githubReadCacheScope == scope {
			cache.removeLocked(key, entry)
		}
	}
	cache.mu.Unlock()
}

func (cache *githubReadCache) putLocked(key githubReadCacheKey, entry githubReadCacheEntry) {
	if previous, found := cache.entries[key]; found {
		cache.bytes -= int64(len(previous.body))
	}
	cache.entries[key] = entry
	cache.bytes += int64(len(entry.body))
	for len(cache.entries) > githubReadCacheMaximumItems || cache.bytes > githubReadCacheMaximumBytes {
		var oldestKey githubReadCacheKey
		var oldest githubReadCacheEntry
		found := false
		for candidateKey, candidate := range cache.entries {
			if !found || candidate.sequence < oldest.sequence {
				oldestKey, oldest, found = candidateKey, candidate, true
			}
		}
		if !found {
			break
		}
		cache.removeLocked(oldestKey, oldest)
	}
}

func (cache *githubReadCache) removeLocked(key githubReadCacheKey, entry githubReadCacheEntry) {
	delete(cache.entries, key)
	cache.bytes -= int64(len(entry.body))
}

func readGitHubResponse(response *http.Response) (githubReadCacheEntry, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubResponse+1))
	if err != nil {
		return githubReadCacheEntry{}, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}
	return githubReadCacheEntry{
		statusCode: response.StatusCode,
		header:     response.Header.Clone(),
		body:       body,
	}, nil
}

func cacheableGitHubReadError(err error) *forge.Error {
	var forgeError *forge.Error
	if !errors.As(err, &forgeError) || forgeError.Status != http.StatusNotFound {
		return nil
	}
	copy := *forgeError
	return &copy
}

func cachedGitHubResponse(entry githubReadCacheEntry) (*http.Response, error) {
	if entry.err != nil {
		copy := *entry.err
		return nil, &copy
	}
	return &http.Response{
		StatusCode:    entry.statusCode,
		Header:        entry.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(entry.body)),
		ContentLength: int64(len(entry.body)),
	}, nil
}

func githubResultMetadata() forge.ResultMetadata {
	return forge.ResultMetadata{CacheControl: githubBrowserCacheControl}
}
