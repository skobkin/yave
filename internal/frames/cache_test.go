package frames

import (
	"image"
	"image/color"
	"testing"
	"time"
)

func solidImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.White)
	return img
}

func TestCachePutGet(t *testing.T) {
	t.Parallel()
	cache := NewCache(1<<20, 1<<20)
	if _, ok := cache.Get(time.Second); ok {
		t.Fatal("empty cache returned a hit")
	}
	first := solidImage(10, 10)
	cache.Put(time.Second, first)
	got, ok := cache.Get(time.Second)
	if !ok || got != first {
		t.Fatalf("expected the stored image, got %v ok=%v", got, ok)
	}
	cache.Put(time.Second, first)
	if _, ok := cache.Get(2 * time.Second); ok {
		t.Fatal("unrelated key returned a hit")
	}
}

func TestCacheByteBudgetEviction(t *testing.T) {
	t.Parallel()
	// Budget holds exactly two 10x10 images (10*10*4 = 400 bytes each).
	cache := NewCache(800, 800)
	first := solidImage(10, 10)
	second := solidImage(10, 10)
	third := solidImage(10, 10)
	cache.Put(time.Second, first)
	cache.Put(2*time.Second, second)
	cache.Put(3*time.Second, third) // evicts the least recent (first)
	if _, ok := cache.Get(time.Second); ok {
		t.Error("oldest entry should have been evicted")
	}
	if _, ok := cache.Get(2 * time.Second); !ok {
		t.Error("middle entry should survive")
	}
	if _, ok := cache.Get(3 * time.Second); !ok {
		t.Error("newest entry should survive")
	}
}

func TestCacheGetPromotesRecency(t *testing.T) {
	t.Parallel()
	cache := NewCache(800, 800)
	cache.Put(time.Second, solidImage(10, 10))
	cache.Put(2*time.Second, solidImage(10, 10))
	_, _ = cache.Get(time.Second) // promote the first entry
	cache.Put(3*time.Second, solidImage(10, 10))
	if _, ok := cache.Get(2 * time.Second); ok {
		t.Error("accessed entry must survive eviction; the untouched one goes")
	}
	if _, ok := cache.Get(time.Second); !ok {
		t.Error("promoted entry should survive")
	}
}

func TestCacheAdmissionCap(t *testing.T) {
	t.Parallel()
	// Per-entry cap below the image cost: nothing is stored.
	cache := NewCache(1<<20, 100) // 100 bytes cap; 10x10 costs 400
	cache.Put(time.Second, solidImage(10, 10))
	if _, ok := cache.Get(time.Second); ok {
		t.Error("oversized entry must be refused")
	}
}

func TestCachePutReplacesAndShrinks(t *testing.T) {
	t.Parallel()
	cache := NewCache(800, 800)
	big := solidImage(10, 10)
	cache.Put(time.Second, big)
	cache.Put(time.Second, solidImage(2, 2)) // replacement frees the old cost
	cache.Put(2*time.Second, solidImage(10, 10))
	if _, ok := cache.Get(time.Second); !ok {
		t.Error("replacement entry should be present")
	}
	if _, ok := cache.Get(2 * time.Second); !ok {
		t.Error("budget must accommodate the shrunk entry plus a new one")
	}
}

func TestCacheClear(t *testing.T) {
	t.Parallel()
	cache := NewCache(1<<20, 1<<20)
	cache.Put(time.Second, solidImage(10, 10))
	cache.Clear()
	if _, ok := cache.Get(time.Second); ok {
		t.Error("Clear must drop all entries")
	}
	cache.Put(2*time.Second, solidImage(10, 10))
	if _, ok := cache.Get(2 * time.Second); !ok {
		t.Error("cache must be usable after Clear")
	}
}

func TestCacheIgnoresNil(t *testing.T) {
	t.Parallel()
	cache := NewCache(1<<20, 1<<20)
	cache.Put(time.Second, nil)
	if _, ok := cache.Get(time.Second); ok {
		t.Error("nil image must not be cached")
	}
}
