package frames

import (
	"container/list"
	"image"
	"sync"
	"time"
)

// Cache is a byte-budgeted LRU of decoded preview frames keyed by
// timestamp. Entry cost is the decoded image's RGBA footprint
// (width × height × 4), so the budget bounds memory rather than entry
// count — preview sizes vary with the source aspect ratio. Get
// promotes recency; Put evicts the least recent entries until the
// budget holds; images whose cost exceeds the per-entry admission cap
// are displayed by the caller but never stored.
type Cache struct {
	mu            sync.Mutex
	maxBytes      int64
	maxEntryBytes int64
	bytes         int64
	order         *list.List // front = most recently used
	items         map[time.Duration]*list.Element
}

type cacheEntry struct {
	key  time.Duration
	img  image.Image
	cost int64
}

// NewCache returns a cache holding at most maxBytes of decoded images,
// refusing entries above maxEntryBytes.
func NewCache(maxBytes, maxEntryBytes int64) *Cache {
	return &Cache{
		maxBytes:      maxBytes,
		maxEntryBytes: maxEntryBytes,
		order:         list.New(),
		items:         make(map[time.Duration]*list.Element),
	}
}

// imageCost approximates the memory an image occupies once decoded
// (RGBA).
func imageCost(img image.Image) int64 {
	b := img.Bounds()
	return int64(b.Dx()) * int64(b.Dy()) * 4
}

// Get returns the cached image for at, if present, promoting it to
// most recently used.
func (c *Cache) Get(at time.Duration) (image.Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[at]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*cacheEntry).img, true
}

// Put stores img under at, evicting least-recent entries to stay
// within the budget. Oversized images (and empty bounds) are ignored.
func (c *Cache) Put(at time.Duration, img image.Image) {
	if img == nil {
		return
	}
	cost := imageCost(img)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.items[at]; ok {
		c.bytes -= existing.Value.(*cacheEntry).cost
		c.order.Remove(existing)
		delete(c.items, at)
	}
	if cost > c.maxEntryBytes || cost > c.maxBytes {
		return
	}
	for c.bytes+cost > c.maxBytes && c.order.Len() > 0 {
		oldest := c.order.Back()
		entry := oldest.Value.(*cacheEntry)
		c.bytes -= entry.cost
		c.order.Remove(oldest)
		delete(c.items, entry.key)
	}
	c.items[at] = c.order.PushFront(&cacheEntry{key: at, img: img, cost: cost})
	c.bytes += cost
}

// Clear drops every cached image.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order.Init()
	c.items = make(map[time.Duration]*list.Element)
	c.bytes = 0
}
