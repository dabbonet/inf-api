package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"orchids-api/internal/config"
	"orchids-api/internal/store"
)



func TestBuildQuotaResponseFields_PuterMonthlyUsage(t *testing.T) {
	t.Parallel()

	acc := &store.Account{
		AccountType:  "puter",
		UsageCurrent: 13494935.4,
		UsageLimit:   25000000,
	}

	fields := buildQuotaResponseFields(acc)
	if got := fields["quota_supported"].(bool); !got {
		t.Fatal("quota_supported=false want true")
	}
	if got := fields["quota_limit"].(float64); got != 25000000 {
		t.Fatalf("quota_limit=%v want 25000000", got)
	}
	if got := fields["quota_remaining"].(float64); got != 13494935.4 {
		t.Fatalf("quota_remaining=%v want 13494935.4", got)
	}
	if got := fields["quota_used"].(float64); got != 11505064.6 {
		t.Fatalf("quota_used=%v want 11505064.6", got)
	}
	if got := fields["quota_unit"].(string); got != "credits" {
		t.Fatalf("quota_unit=%q want credits", got)
	}
}



func TestApplyAccountStatusFromError(t *testing.T) {
	t.Parallel()

	acc := &store.Account{}
	applyAccountStatusFromError(acc, errors.New("unexpected status code 429: too many requests"))
	if acc.StatusCode != "429" {
		t.Fatalf("StatusCode=%q want 429", acc.StatusCode)
	}
	if acc.LastAttempt.IsZero() {
		t.Fatal("LastAttempt should be set")
	}

	unknown := &store.Account{}
	applyAccountStatusFromError(unknown, errors.New("plain failure"))
	if unknown.StatusCode != "" {
		t.Fatalf("StatusCode=%q want empty", unknown.StatusCode)
	}

	noActiveSession := &store.Account{}
	applyAccountStatusFromError(noActiveSession, errors.New("no active sessions found"))
	if noActiveSession.StatusCode != "401" {
		t.Fatalf("StatusCode=%q want 401", noActiveSession.StatusCode)
	}
}



func TestHandleAccountByID_PutAllowsSameAccountCredential(t *testing.T) {
	a, s, cleanup := newTestAPI(t)
	defer cleanup()

	acc := &store.Account{
		AccountType:   "puter",
		SessionCookie: "puter-token-1",
		Enabled:       true,
		Name:          "before",
	}
	if err := s.CreateAccount(context.Background(), acc); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/accounts/"+strconv.FormatInt(acc.ID, 10), strings.NewReader(`{"account_type":"puter","session_cookie":"puter-token-1","enabled":true,"name":"after"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	a.HandleAccountByID(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func newTestAPI(t *testing.T) (*API, *store.Store, func()) {
	t.Helper()

	mini := miniredis.RunT(t)
	s, err := store.New(store.Options{
		StoreMode:   "redis",
		RedisAddr:   mini.Addr(),
		RedisPrefix: "api_test:",
	})
	if err != nil {
		mini.Close()
		t.Fatalf("store.New() error = %v", err)
	}

	return New(s, "", "", &config.Config{}), s, func() {
		_ = s.Close()
		mini.Close()
	}
}

