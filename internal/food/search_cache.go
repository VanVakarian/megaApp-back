package food

import "sync"

// searchResultsCache holds the ranked ids per query for one search index snapshot. It lives and dies
// with the snapshot, so a catalogue change needs no separate cache clearing.
type searchResultsCache struct {
	mu     sync.RWMutex
	values map[string][]int64
}

func newSearchResultsCache() *searchResultsCache {
	return &searchResultsCache{values: make(map[string][]int64)}
}

func (c *searchResultsCache) Get(query string) ([]int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	value, ok := c.values[query]
	if !ok {
		return nil, false
	}

	result := make([]int64, len(value))
	copy(result, value)
	return result, true
}

func (c *searchResultsCache) Set(query string, ids []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	value := make([]int64, len(ids))
	copy(value, ids)
	c.values[query] = value
}
