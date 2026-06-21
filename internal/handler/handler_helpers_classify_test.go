package handler

import (
	"testing"
	"time"
)

func TestClassifyUpstreamErrorCreditsExhausted(t *testing.T) {
	t.Parallel()

	errClass := classifyUpstreamError("upstream error: no remaining quota: You have run out of credits.")
	if errClass.Category != "quota_exhausted" {
		t.Fatalf("expected quota_exhausted category, got %q", errClass.Category)
	}
	if !errClass.Retryable {
		t.Fatal("expected credits exhausted to be retryable")
	}
	if !errClass.SwitchAccount {
		t.Fatal("expected credits exhausted to trigger account switch")
	}
}

func TestShouldRetryCurrentAccountWhenNoAlternative_RateLimit(t *testing.T) {
	t.Parallel()

	if shouldRetryCurrentAccountWhenNoAlternative("rate_limit") {
		t.Fatal("expected rate_limit to stop retrying the same account when no alternative exists")
	}
}

func TestShouldRetryCurrentAccountWhenNoAlternative_QuotaExhausted(t *testing.T) {
	t.Parallel()

	if shouldRetryCurrentAccountWhenNoAlternative("quota_exhausted") {
		t.Fatal("expected quota_exhausted to stop retrying the same account when no alternative exists")
	}
}

func TestShouldRetryCurrentAccountWhenNoAlternative_ModelUnavailable(t *testing.T) {
	t.Parallel()

	if !shouldRetryCurrentAccountWhenNoAlternative("model_unavailable") {
		t.Fatal("expected model_unavailable to retry the current account when no alternative exists")
	}
}

func TestComputeRetryDelay_RateLimitMinDelay(t *testing.T) {
	t.Parallel()

	delay := computeRetryDelay(1*time.Second, 1, "rate_limit")
	if delay < 2*time.Second {
		t.Fatalf("expected rate_limit delay >= 2s, got %v", delay)
	}
}

func TestComputeRetryDelay_QuotaExhaustedMinDelay(t *testing.T) {
	t.Parallel()

	delay := computeRetryDelay(1*time.Second, 1, "quota_exhausted")
	if delay < 2*time.Second {
		t.Fatalf("expected quota_exhausted delay >= 2s, got %v", delay)
	}
}

func TestComputeRetryDelay_QuotaExhaustedExponential(t *testing.T) {
	t.Parallel()

	delay1 := computeRetryDelay(1*time.Second, 1, "quota_exhausted")
	delay3 := computeRetryDelay(1*time.Second, 3, "quota_exhausted")
	if delay3 <= delay1 {
		t.Fatalf("expected exponential growth: attempt1=%v attempt3=%v", delay1, delay3)
	}
	if delay3 > 30*time.Second {
		t.Fatalf("expected delay capped at 30s, got %v", delay3)
	}
}

func TestComputeRetryDelay_QuotaExhaustedMaxCap(t *testing.T) {
	t.Parallel()

	delay := computeRetryDelay(1*time.Second, 20, "quota_exhausted")
	if delay > 30*time.Second {
		t.Fatalf("expected delay capped at 30s, got %v", delay)
	}
}

func TestClassifyUpstreamError_CreditsExhaustedIsQuotaExhausted(t *testing.T) {
	t.Parallel()

	errClass := classifyUpstreamError("upstream error: no remaining quota: You have run out of credits.")
	if errClass.Category != "quota_exhausted" {
		t.Fatalf("expected quota_exhausted category, got %q", errClass.Category)
	}
	if !errClass.Retryable {
		t.Fatal("expected credits exhausted to be retryable")
	}
	if !errClass.SwitchAccount {
		t.Fatal("expected credits exhausted to trigger account switch")
	}
}
