package browserbridge

import (
	"testing"
	"time"
)

func TestNewTabBindingRecordsBoundTime(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	binding := NewTabBinding(now)
	if binding.boundAt != now {
		t.Fatalf("boundAt = %v, want %v", binding.boundAt, now)
	}
	if binding.revision != 1 {
		t.Fatalf("revision = %d, want 1", binding.revision)
	}
	if binding.Expired(now, time.Minute) {
		t.Fatal("freshly bound tab reported as expired")
	}
}

func TestTouchBumpsRevisionAndBoundTime(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	binding := NewTabBinding(now)
	later := now.Add(time.Minute)
	binding.Touch(later)
	if binding.revision != 2 {
		t.Fatalf("revision = %d, want 2", binding.revision)
	}
	if binding.boundAt != later {
		t.Fatalf("boundAt = %v, want %v", binding.boundAt, later)
	}
}

func TestExpiredBeforeAndAfterMaxIdle(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	binding := NewTabBinding(now)
	if binding.Expired(now.Add(4*time.Minute), 5*time.Minute) {
		t.Fatal("binding expired before maxIdle elapsed")
	}
	if !binding.Expired(now.Add(6*time.Minute), 5*time.Minute) {
		t.Fatal("binding did not expire after maxIdle elapsed")
	}
}

func TestTouchExtendsTheIdleWindow(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	binding := NewTabBinding(now)
	binding.Touch(now.Add(4 * time.Minute))
	if binding.Expired(now.Add(8*time.Minute), 5*time.Minute) {
		t.Fatal("touch did not extend the idle window")
	}
}

func TestClearZeroesState(t *testing.T) {
	binding := NewTabBinding(time.Now())
	binding.Clear()
	if !binding.boundAt.IsZero() || binding.revision != 0 {
		t.Fatalf("clear left state: boundAt=%v revision=%d", binding.boundAt, binding.revision)
	}
	if !binding.Expired(time.Now(), time.Hour) {
		t.Fatal("cleared binding should be treated as expired")
	}
}

func TestNilTabBindingIsSafe(t *testing.T) {
	var binding *TabBinding
	binding.Touch(time.Now())
	binding.Clear()
	if !binding.Expired(time.Now(), time.Hour) {
		t.Fatal("nil binding should be expired")
	}
}
