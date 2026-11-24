package ui

import (
	"reflect"
	"testing"
	"time"
)

func secs(vals ...float64) []time.Duration {
	out := make([]time.Duration, len(vals))
	for i, v := range vals {
		out[i] = time.Duration(v * float64(time.Second))
	}
	return out
}

func TestKeyframeIndexBeforeAfter(t *testing.T) {
	var k keyframeIndex
	// A scan of [7.5s, 13s] returns the keyframe the seek landed on (6s).
	k.add(7500*time.Millisecond, 13*time.Second, secs(6, 9, 12))

	tests := []struct {
		name         string
		at           float64
		wantBefore   float64
		okBefore     bool
		wantAfter    float64
		okAfter      bool
		beforeAnswer string
	}{
		{name: "mid-GOP", at: 10, wantBefore: 9, okBefore: true, wantAfter: 12, okAfter: true},
		{name: "exactly on a keyframe", at: 9, wantBefore: 9, okBefore: true, wantAfter: 9, okAfter: true},
		{name: "before the first returned keyframe, inside the span", at: 6.5, wantBefore: 6, okBefore: true, wantAfter: 9, okAfter: true},
		{name: "after the last keyframe, inside the span", at: 12.5, wantBefore: 12, okBefore: true, wantAfter: 0, okAfter: false},
		{name: "before the span", at: 5, wantBefore: 0, okBefore: false, wantAfter: 0, okAfter: false},
		{name: "after the span", at: 20, wantBefore: 0, okBefore: false, wantAfter: 0, okAfter: false},
	}
	for _, tt := range tests {
		gotB, okB := k.before(secs(tt.at)[0])
		if okB != tt.okBefore || (okB && gotB != secs(tt.wantBefore)[0]) {
			t.Errorf("%s: before(%v) = (%s, %v), want (%vs, %v)", tt.name, tt.at, gotB, okB, tt.wantBefore, tt.okBefore)
		}
		gotA, okA := k.after(secs(tt.at)[0])
		if okA != tt.okAfter || (okA && gotA != secs(tt.wantAfter)[0]) {
			t.Errorf("%s: after(%v) = (%s, %v), want (%vs, %v)", tt.name, tt.at, gotA, okA, tt.wantAfter, tt.okAfter)
		}
	}
}

func TestKeyframeIndexSpanStartRules(t *testing.T) {
	t.Run("the seek keyframe extends coverage backwards", func(t *testing.T) {
		var k keyframeIndex
		k.add(7*time.Second, 13*time.Second, secs(6, 9))
		if !k.covers(6*time.Second, 13*time.Second) || k.covers(5*time.Second, 13*time.Second) {
			t.Errorf("spans = %+v", k.spans)
		}
		if got, ok := k.before(7 * time.Second); !ok || got != 6*time.Second {
			t.Errorf("before(7s) = %s, %v, want 6s", got, ok)
		}
	})
	t.Run("a first keyframe after a positive from leaves the earlier part unknown", func(t *testing.T) {
		var k keyframeIndex
		k.add(7*time.Second, 13*time.Second, secs(9, 12))
		if _, ok := k.before(8 * time.Second); ok {
			t.Error("8s precedes the first seen keyframe; the preceding keyframe is unknown")
		}
		if got, ok := k.before(10 * time.Second); !ok || got != 9*time.Second {
			t.Errorf("before(10s) = %s, %v", got, ok)
		}
	})
	t.Run("the start of the input covers from zero", func(t *testing.T) {
		var k keyframeIndex
		k.add(0, 5*time.Second, secs(0.04, 3))
		if !k.covers(0, 5*time.Second) {
			t.Errorf("spans = %+v", k.spans)
		}
		if _, ok := k.before(0); ok {
			t.Error("no keyframe at or before 0 when the first one is at 0.04s")
		}
		if got, ok := k.after(0); !ok || got != 40*time.Millisecond {
			t.Errorf("after(0) = %s, %v", got, ok)
		}
	})
	t.Run("a window with no keyframes is covered but answers nothing", func(t *testing.T) {
		var k keyframeIndex
		k.add(10*time.Second, 11*time.Second, nil)
		if !k.covers(10*time.Second, 11*time.Second) {
			t.Error("the empty window must count as fetched")
		}
		if _, ok := k.before(10500 * time.Millisecond); ok {
			t.Error("no keyframe was seen")
		}
	})
	t.Run("keyframes outside the span are discarded", func(t *testing.T) {
		var k keyframeIndex
		k.add(7*time.Second, 10*time.Second, secs(6, 9, 11))
		if got := k.all(); !reflect.DeepEqual(got, secs(6, 9)) {
			t.Errorf("all = %v", got)
		}
	})
	t.Run("an inverted window is ignored", func(t *testing.T) {
		var k keyframeIndex
		k.add(10*time.Second, 5*time.Second, secs(6))
		if len(k.spans) != 0 {
			t.Error("inverted window must not add a span")
		}
	})
}

func TestKeyframeIndexMerging(t *testing.T) {
	var k keyframeIndex
	k.add(0, 10*time.Second, secs(0, 3, 6, 9))
	k.add(20*time.Second, 30*time.Second, secs(18, 21, 24, 27))
	if len(k.spans) != 2 {
		t.Fatalf("disjoint windows must stay separate: %+v", k.spans)
	}
	if k.covers(5*time.Second, 25*time.Second) {
		t.Error("the gap between the windows is not covered")
	}

	// A window bridging the gap merges all three.
	k.add(9*time.Second, 21*time.Second, secs(9, 12, 15, 18, 21))
	if len(k.spans) != 1 {
		t.Fatalf("bridging window must merge: %+v", k.spans)
	}
	want := secs(0, 3, 6, 9, 12, 15, 18, 21, 24, 27)
	if got := k.all(); !reflect.DeepEqual(got, want) {
		t.Errorf("merged keyframes = %v, want %v", got, want)
	}
	if !k.covers(5*time.Second, 25*time.Second) {
		t.Error("the merged span must cover the former gap")
	}

	// Touching windows merge too.
	var touch keyframeIndex
	touch.add(0, 5*time.Second, secs(0, 3))
	touch.add(5*time.Second, 10*time.Second, secs(3, 6, 9))
	if len(touch.spans) != 1 {
		t.Errorf("touching windows must merge: %+v", touch.spans)
	}
	if got := touch.all(); !reflect.DeepEqual(got, secs(0, 3, 6, 9)) {
		t.Errorf("touching merge = %v (duplicates must collapse)", got)
	}
}

func TestKeyframeIndexReset(t *testing.T) {
	var k keyframeIndex
	k.add(0, 10*time.Second, secs(0, 5))
	k.reset()
	if k.covers(0, 1) || len(k.all()) != 0 {
		t.Error("reset must forget everything")
	}
}

func TestKeyframeIndexEvictsTheFarthestSpans(t *testing.T) {
	var k keyframeIndex
	dense := func(from time.Duration, n int) []time.Duration {
		out := make([]time.Duration, n)
		for i := range out {
			out[i] = from + time.Duration(i)*time.Millisecond
		}
		return out
	}
	k.add(0, 30*time.Second, dense(0, keyframeCap/2))
	k.add(1000*time.Second, 1030*time.Second, dense(1000*time.Second, keyframeCap/2))
	k.add(5000*time.Second, 5030*time.Second, dense(5000*time.Second, keyframeCap/2))
	if k.total() > keyframeCap {
		t.Fatalf("total %d exceeds the cap %d", k.total(), keyframeCap)
	}
	if !k.covers(5000*time.Second, 5001*time.Second) {
		t.Error("the newest span must survive eviction")
	}
	if k.covers(0, time.Second) {
		t.Error("the farthest span must be the one dropped")
	}
}
