package store

import (
	"context"
	"github.com/goccy/go-json"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestModelStatus_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    ModelStatus
		enabled bool
	}{
		{name: "bool true", input: `true`, want: ModelStatusAvailable, enabled: true},
		{name: "bool false", input: `false`, want: ModelStatusOffline, enabled: false},
		{name: "available", input: `"available"`, want: ModelStatusAvailable, enabled: true},
		{name: "maintenance", input: `"maintenance"`, want: ModelStatusMaintenance, enabled: false},
		{name: "offline", input: `"offline"`, want: ModelStatusOffline, enabled: false},
		{name: "unknown", input: `"something"`, want: ModelStatusOffline, enabled: false},
		{name: "null", input: `null`, want: ModelStatusOffline, enabled: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var s ModelStatus
			if err := json.Unmarshal([]byte(tt.input), &s); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if s != tt.want {
				t.Fatalf("got %q want %q", s, tt.want)
			}
			if s.Enabled() != tt.enabled {
				t.Fatalf("enabled=%v want %v", s.Enabled(), tt.enabled)
			}
		})
	}
}

func TestModelStatus_MarshalJSON(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(ModelStatusAvailable)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(b) != `"available"` {
		t.Fatalf("got %s want %s", string(b), `"available"`)
	}
}

func TestGetModelByChannelAndModelID_AllowsDuplicateModelIDsAcrossChannels(t *testing.T) {
	t.Parallel()

	mini := miniredis.RunT(t)
	s, err := New(Options{
		StoreMode:   "redis",
		RedisAddr:   mini.Addr(),
		RedisDB:     0,
		RedisPrefix: "test:",
	})
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		mini.Close()
	})

	ctx := context.Background()

	puterModel, err := s.GetModelByChannelAndModelID(ctx, "puter", "claude-opus-4-5")
	if err != nil {
		t.Fatalf("GetModelByChannelAndModelID(puter) error = %v", err)
	}
	if puterModel.Channel != "Puter" {
		t.Fatalf("puter model channel = %q, want Puter", puterModel.Channel)
	}

	dupe := &Model{
		Channel: "Codebuff",
		ModelID: "claude-opus-4-5",
		Name:    "claude-opus-4-5 (codebuff)",
		Status:  ModelStatusAvailable,
	}
	if err := s.CreateModel(ctx, dupe); err != nil {
		t.Fatalf("CreateModel(codebuff) error = %v", err)
	}
	codebuffModel, err := s.GetModelByChannelAndModelID(ctx, "codebuff", "claude-opus-4-5")
	if err != nil {
		t.Fatalf("GetModelByChannelAndModelID(codebuff) error = %v", err)
	}
	if codebuffModel.Channel != "Codebuff" {
		t.Fatalf("codebuff model channel = %q, want Codebuff", codebuffModel.Channel)
	}
	if puterModel.ID == codebuffModel.ID {
		t.Fatalf("expected different records across channels, got same id %q", puterModel.ID)
	}
}

func TestStoreNew_PreservesExistingModelList(t *testing.T) {
	t.Parallel()

	mini := miniredis.RunT(t)
	opts := Options{
		StoreMode:   "redis",
		RedisAddr:   mini.Addr(),
		RedisDB:     0,
		RedisPrefix: "test:",
	}
	s, err := New(opts)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}

	ctx := context.Background()
	model, err := s.GetModelByChannelAndModelID(ctx, "puter", "claude-opus-4-5")
	if err != nil {
		t.Fatalf("GetModelByChannelAndModelID() error = %v", err)
	}
	if err := s.DeleteModel(ctx, model.ID); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	_ = s.Close()

	s, err = New(opts)
	if err != nil {
		t.Fatalf("store.New() second error = %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		mini.Close()
	})

	if _, err := s.GetModelByChannelAndModelID(ctx, "puter", "claude-opus-4-5"); err == nil {
		t.Fatal("expected deleted model to stay deleted after store restart")
	}
}
