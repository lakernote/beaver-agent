package server

import (
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterRepeatedFailures(t *testing.T) {
	var limiter loginLimiter
	now := time.Now()
	for i := 0; i < loginMaxFailures; i++ {
		if limiter.retryAfter("1.2.3.4", now) != 0 {
			t.Fatalf("第 %d 次失败前不应被限制", i)
		}
		limiter.fail("1.2.3.4", now)
	}
	if limiter.retryAfter("1.2.3.4", now) == 0 {
		t.Fatal("连续失败后应被限制")
	}
	if limiter.retryAfter("5.6.7.8", now) != 0 {
		t.Fatal("其他来源不应受影响")
	}
	if limiter.retryAfter("1.2.3.4", now.Add(loginWindow+time.Second)) != 0 {
		t.Fatal("窗口过后应恢复")
	}
	limiter.fail("9.9.9.9", now)
	limiter.reset("9.9.9.9")
	if len(limiter.failures["9.9.9.9"]) != 0 {
		t.Fatal("登录成功后应清除失败记录")
	}
}
