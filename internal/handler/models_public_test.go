package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"orchids-api/internal/store"
)

func TestHandleModelByID_HidesOfflineModel(t *testing.T) {
	h, s, mini := setupModelValidationHandler(t)
	defer func() {
		_ = s.Close()
		mini.Close()
	}()

	if err := s.CreateModel(context.Background(), &store.Model{
		Channel: "Warp",
		ModelID: "offline-only-model",
		Name:    "Offline Only",
		Status:  store.ModelStatusOffline,
	}); err != nil {
		t.Fatalf("CreateModel() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/warp/v1/models/offline-only-model", nil)
	rec := httptest.NewRecorder()

	h.HandleModelByID(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestHandleModelByID_HidesUnsupportedGrokModel(t *testing.T) {
	h, s, mini := setupModelValidationHandler(t)
	defer func() {
		_ = s.Close()
		mini.Close()
	}()

	req := httptest.NewRequest(http.MethodGet, "http://example.com/grok/v1/models/grok-4.1", nil)
	rec := httptest.NewRecorder()

	h.HandleModelByID(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestHandleModelByID_ReturnsVerifiedDynamicGrokModel(t *testing.T) {
	h, s, mini := setupModelValidationHandler(t)
	defer func() {
		_ = s.Close()
		mini.Close()
	}()

	if err := s.CreateModel(context.Background(), &store.Model{
		Channel:  "Grok",
		ModelID:  "grok-future-6",
		Name:     "grok-future-6",
		Status:   store.ModelStatusAvailable,
		Verified: true,
	}); err != nil {
		t.Fatalf("CreateModel() error = %v", err)
	}
	if err := s.CreateAccount(context.Background(), &store.Account{
		AccountType:  "grok",
		ClientCookie: "sso=basic-token",
		Subscription: "basic",
		Enabled:      true,
	}); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/grok/v1/models/grok-future-6", nil)
	rec := httptest.NewRecorder()

	h.HandleModelByID(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

