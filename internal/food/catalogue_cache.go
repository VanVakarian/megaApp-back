package food

import (
	"sync"
	"time"
)

// CatalogueCache holds the whole catalogue (minus per-entry ImageVersion, which is resolved
// fresh on every read from ImageVersionProvider — cheap, in-memory, and lets image generation
// stay decoupled from cache invalidation) plus a version counter. The counter only moves on an
// actual write (Invalidate, called from SaveProduct/DeleteProduct) — it is the cheap "did the
// shared catalogue change" signal GET /api/food/catalogue/version answers for reconnect catch-up.
// It starts from the launch time in milliseconds, so a restart never rewinds it to a value a
// client has already seen (writes between restarts are far fewer than milliseconds elapsed).
type CatalogueCache struct {
	mu      sync.RWMutex
	version int64
	entries map[int64]CatalogueEntry // nil when invalidated (next read recomputes)
}

func NewCatalogueCache() *CatalogueCache {
	return &CatalogueCache{version: time.Now().UnixMilli()}
}

func (c *CatalogueCache) Get() (map[int64]CatalogueEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.entries == nil {
		return nil, false
	}
	return cloneCatalogueEntries(c.entries), true
}

func (c *CatalogueCache) Set(entries map[int64]CatalogueEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = cloneCatalogueEntries(entries)
}

func (c *CatalogueCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.version++
	c.entries = nil
}

func (c *CatalogueCache) Version() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.version
}

func cloneCatalogueEntries(input map[int64]CatalogueEntry) map[int64]CatalogueEntry {
	result := make(map[int64]CatalogueEntry, len(input))
	for id, entry := range input {
		result[id] = entry
	}
	return result
}
