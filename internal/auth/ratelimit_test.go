package auth

import (
	"testing"
	"time"
)

func TestRateLimiter_AllowsUnderQuota(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.Allow("ip1") {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
}

func TestRateLimiter_BlocksOverQuota(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	_ = rl.Allow("ip1")
	_ = rl.Allow("ip1")
	if rl.Allow("ip1") {
		t.Fatal("3rd attempt should be blocked")
	}
}

func TestRateLimiter_PerIPSeparate(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)
	if !rl.Allow("ip1") {
		t.Fatal("ip1 first allowed")
	}
	if !rl.Allow("ip2") {
		t.Fatal("ip2 first allowed")
	}
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(1, 50*time.Millisecond)
	_ = rl.Allow("ip1")
	if rl.Allow("ip1") {
		t.Fatal("second should be blocked")
	}
	time.Sleep(80 * time.Millisecond)
	if !rl.Allow("ip1") {
		t.Fatal("after refill, should be allowed")
	}
}
