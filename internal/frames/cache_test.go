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

const w = 640 // the preview width the existing cases use

func TestCachePutGet(t *testing.T) {
	t.Parallel()
	cache := NewCache(1<<20, 1<<20)
	if _, ok := cache.Get(time.Second, w); ok {
		t.Fatal("empty cache returned a hit")
	}
	first := solidImage(10, 10)
	cache.Put(time.Second, w, first)
	got, ok := cache.Get(time.Second, w)
	if !ok || got != first {
		t.Fatalf("expected the stored image, got %v ok=%v", got, ok)
	}
	cache.Put(time.Second, w, first)
	if _, ok := cache.Get(2*time.Second, w); ok {
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
	cache.Put(time.Second, w, first)
	cache.Put(2*time.Second, w, second)
	cache.Put(3*time.Second, w, third) // evicts the least recent (first)
	if _, ok := cache.Get(time.Second, w); ok {
		t.Error("oldest entry should have been evicted")
	}
	if _, ok := cache.Get(2*time.Second, w); !ok {
		t.Error("middle entry should survive")
	}
	if _, ok := cache.Get(3*time.Second, w); !ok {
		t.Error("newest entry should survive")
	}
}

func TestCacheGetPromotesRecency(t *testing.T) {
	t.Parallel()
	cache := NewCache(800, 800)
	cache.Put(time.Second, w, solidImage(10, 10))
	cache.Put(2*time.Second, w, solidImage(10, 10))
	_, _ = cache.Get(time.Second, w) // promote the first entry
	cache.Put(3*time.Second, w, solidImage(10, 10))
	if _, ok := cache.Get(2*time.Second, w); ok {
		t.Error("accessed entry must survive eviction; the untouched one goes")
	}
	if _, ok := cache.Get(time.Second, w); !ok {
		t.Error("promoted entry should survive")
	}
}

func TestCacheAdmissionCap(t *testing.T) {
	t.Parallel()
	// Per-entry cap below the image cost: nothing is stored.
	cache := NewCache(1<<20, 100) // 100 bytes cap; 10x10 costs 400
	cache.Put(time.Second, w, solidImage(10, 10))
	if _, ok := cache.Get(time.Second, w); ok {
		t.Error("oversized entry must be refused")
	}
}

func TestCachePutReplacesAndShrinks(t *testing.T) {
	t.Parallel()
	cache := NewCache(800, 800)
	big := solidImage(10, 10)
	cache.Put(time.Second, w, big)
	cache.Put(time.Second, w, solidImage(2, 2)) // replacement frees the old cost
	cache.Put(2*time.Second, w, solidImage(10, 10))
	if _, ok := cache.Get(time.Second, w); !ok {
		t.Error("replacement entry should be present")
	}
	if _, ok := cache.Get(2*time.Second, w); !ok {
		t.Error("budget must accommodate the shrunk entry plus a new one")
	}
}

func TestCacheClear(t *testing.T) {
	t.Parallel()
	cache := NewCache(1<<20, 1<<20)
	cache.Put(time.Second, w, solidImage(10, 10))
	cache.Clear()
	if _, ok := cache.Get(time.Second, w); ok {
		t.Error("Clear must drop all entries")
	}
	cache.Put(2*time.Second, w, solidImage(10, 10))
	if _, ok := cache.Get(2*time.Second, w); !ok {
		t.Error("cache must be usable after Clear")
	}
}

func TestCacheIgnoresNil(t *testing.T) {
	t.Parallel()
	cache := NewCache(1<<20, 1<<20)
	cache.Put(time.Second, w, nil)
	if _, ok := cache.Get(time.Second, w); ok {
		t.Error("nil image must not be cached")
	}
}

func TestCacheKeysFramesByDecodeWidth(t *testing.T) {
	t.Parallel()
	cache := NewCache(1<<20, 1<<20)
	small, large := solidImage(10, 10), solidImage(20, 20)
	cache.Put(time.Second, 640, small)
	if _, ok := cache.Get(time.Second, 1920); ok {
		t.Fatal("a 640px frame must not satisfy a 1920px request for the same instant")
	}
	cache.Put(time.Second, 1920, large)
	if got, ok := cache.Get(time.Second, 640); !ok || got != small {
		t.Errorf("the 640px frame was lost: %v %v", got, ok)
	}
	if got, ok := cache.Get(time.Second, 1920); !ok || got != large {
		t.Errorf("the 1920px frame was lost: %v %v", got, ok)
	}
	// Replacing one width frees only that entry's cost.
	cache.Put(time.Second, 640, solidImage(2, 2))
	if got, ok := cache.Get(time.Second, 1920); !ok || got != large {
		t.Error("replacing the 640px entry disturbed the 1920px one")
	}
}
