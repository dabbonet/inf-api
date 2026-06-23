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
		Channel: "Puter",
		ModelID: "offline-only-model",
		Name:    "Offline Only",
		Status:  store.ModelStatusOffline,
	}); err != nil {
		t.Fatalf("CreateModel() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/puter/v1/models/offline-only-model", nil)
	rec := httptest.NewRecorder()

	h.HandleModelByID(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}
