package encoder

import (
	"sort"
	"sync"

	"git.skobk.in/skobkin/yave/internal/model"
)

// Registry holds the curated set of encoders known to yave. It is a
// thin wrapper around a name -> Encoder map; the constructor sorts
// All() and VideoNames()/AudioNames() by priority descending so the UI
// can present encoders in a stable order.
//
// The default registry is process-global and immutable after init. To
// add or remove encoders at startup, call Replace on a fresh registry
// before any goroutine reads from DefaultRegistry.
type Registry struct {
	mu       sync.RWMutex
	encoders map[string]Encoder
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{encoders: make(map[string]Encoder)}
}

// Add inserts an encoder. If an encoder with the same Name is already
// present it is replaced.
func (r *Registry) Add(e Encoder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.encoders[e.Name()] = e
}

// Get returns the encoder registered under name, if any.
func (r *Registry) Get(name string) (Encoder, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.encoders[name]
	return e, ok
}

// All returns every registered encoder ordered by priority descending.
// Equal priorities fall back to name ascending for determinism.
func (r *Registry) All() []Encoder {
	r.mu.RLock()
	out := make([]Encoder, 0, len(r.encoders))
	for _, e := range r.encoders {
		out = append(out, e)
	}
	r.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority() != out[j].Priority() {
			return out[i].Priority() > out[j].Priority()
		}
		return out[i].Name() < out[j].Name()
	})
	return out
}

// VideoNames returns the registered video encoders' names, sorted by
// priority descending then name ascending.
func (r *Registry) VideoNames() []string {
	all := r.All()
	names := make([]string, 0, len(all))
	for _, e := range all {
		if e.Media() == MediaVideo {
			names = append(names, e.Name())
		}
	}
	return names
}

// AudioNames returns the registered audio encoders' names, sorted by
// priority descending then name ascending.
func (r *Registry) AudioNames() []string {
	all := r.All()
	names := make([]string, 0, len(all))
	for _, e := range all {
		if e.Media() == MediaAudio {
			names = append(names, e.Name())
		}
	}
	return names
}

// Model adapts an Encoder into the model.Encoder struct that
// Capabilities.Encoders stores. Returns false if name is unknown.
func (r *Registry) Model(name string) (model.Encoder, bool) {
	e, ok := r.Get(name)
	if !ok {
		return model.Encoder{}, false
	}
	return model.Encoder{
		Name:     e.Name(),
		Label:    e.Label(),
		Media:    e.Media(),
		Kind:     e.Kind(),
		Priority: e.Priority(),
	}, true
}

// Replace swaps the entire encoder set. Intended for tests.
func (r *Registry) Replace(encoders []Encoder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.encoders = make(map[string]Encoder, len(encoders))
	for _, e := range encoders {
		r.encoders[e.Name()] = e
	}
}

var (
	defaultOnce   sync.Once
	defaultReg    *Registry
)

// DefaultRegistry returns the process-wide curated encoder set. It is
// populated lazily by registerDefaults on first call.
func DefaultRegistry() *Registry {
	defaultOnce.Do(func() {
		defaultReg = NewRegistry()
		registerDefaults(defaultReg)
	})
	return defaultReg
}