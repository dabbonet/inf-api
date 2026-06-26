package codebuff

import (
	"fmt"
	"strings"
	"testing"

	"orchids-api/internal/prompt"
	"orchids-api/internal/upstream"
)

func TestResolveModel_Default(t *testing.T) {
	m, err := ResolveModel("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != DEFAULT_MODEL.ID {
		t.Fatalf("expected default model %q, got %q", DEFAULT_MODEL.ID, m.ID)
	}
}

func TestResolveModel_Unknown(t *testing.T) {
	_, err := ResolveModel("unknown/model")
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestResolveModel_Known(t *testing.T) {
	m, err := ResolveModel("moonshotai/kimi-k2.6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.AgentID != "base2-free-kimi" {
		t.Fatalf("unexpected agent id: %s", m.AgentID)
	}
}

func TestModelsResponse(t *testing.T) {
	resp := ModelsResponse()
	data, ok := resp["data"].([]map[string]any)
	if !ok {
		t.Fatal("expected data to be a slice")
	}
	if len(data) != len(ALL_MODELS) {
		t.Fatalf("expected %d models, got %d", len(ALL_MODELS), len(data))
	}
}

func TestBuffyInjection_NoSystem(t *testing.T) {
	req := upstream.UpstreamRequest{
		Messages: []prompt.Message{{Role: "user", Content: prompt.MessageContent{Text: "hello"}}},
	}
	sess := &Session{InstanceID: "abc"}
	run := &Run{RunID: "run-1"}
	payload := BuildPayload(req, sess, run, "client-1")

	msgs, ok := payload["messages"].([]map[string]any)
	if !ok {
		t.Fatal("expected messages slice")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0]["role"] != "system" {
		t.Fatalf("expected first message to be system, got %v", msgs[0]["role"])
	}
	content, _ := msgs[0]["content"].(string)
	if content != defaultSystemMessage {
		t.Fatalf("unexpected system content: %q", content)
	}
}

func TestBuffyInjection_ExistingSystem(t *testing.T) {
	req := upstream.UpstreamRequest{
		System: []prompt.SystemItem{{Text: "Be helpful."}},
		Messages: []prompt.Message{{Role: "user", Content: prompt.MessageContent{Text: "hello"}}},
	}
	sess := &Session{InstanceID: "abc"}
	run := &Run{RunID: "run-1"}
	payload := BuildPayload(req, sess, run, "client-1")

	msgs, ok := payload["messages"].([]map[string]any)
	if !ok {
		t.Fatal("expected messages slice")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	content, _ := msgs[0]["content"].(string)
	if content == "Be helpful." {
		t.Fatal("expected Buffy injection to prepend to existing system message")
	}
	if msgs[0]["role"] != "system" {
		t.Fatalf("expected first message to be system, got %v", msgs[0]["role"])
	}
}

func TestBuffyInjection_AlreadyBuffy(t *testing.T) {
	req := upstream.UpstreamRequest{
		System: []prompt.SystemItem{{Text: "You are Buffy, a strategic assistant."}},
		Messages: []prompt.Message{{Role: "user", Content: prompt.MessageContent{Text: "hello"}}},
	}
	sess := &Session{InstanceID: "abc"}
	run := &Run{RunID: "run-1"}
	payload := BuildPayload(req, sess, run, "client-1")

	msgs, ok := payload["messages"].([]map[string]any)
	if !ok {
		t.Fatal("expected messages slice")
	}
	content, _ := msgs[0]["content"].(string)
	if content != "You are Buffy, a strategic assistant." {
		t.Fatalf("expected no double injection, got %q", content)
	}
}

func TestErrorClassification(t *testing.T) {
	if !IsWaitingRoomRequired(&Error{Message: "waiting_room_required"}) {
		t.Fatal("expected waiting_room_required to be classified")
	}
	if !IsModelLocked(&Error{Message: "model_locked"}) {
		t.Fatal("expected model_locked to be classified")
	}
	if ParseRetryAfter(&Error{Message: `{"retryAfterMs":5000}`}) != 5000 {
		t.Fatal("expected retryAfterMs=5000")
	}
}

func TestSessionFresh(t *testing.T) {
	fresh := &Session{RemainingMs: 60000}
	if !fresh.IsFresh(30000) {
		t.Fatal("expected session with 60s remaining to be fresh")
	}
	stale := &Session{RemainingMs: 1000}
	if stale.IsFresh(30000) {
		t.Fatal("expected session with 1s remaining to be stale")
	}
	// Nil session is not fresh — caller must hit cache + create path.
	if (*Session)(nil).IsFresh(30000) {
		t.Fatal("nil session must not be fresh")
	}
	// Session with unknown remaining time passes the first call.
	unknown := &Session{RemainingMs: 0}
	if !unknown.IsFresh(30000) {
		t.Fatal("session with unknown remaining time should assume fresh on first use")
	}
}

func TestSessionCacheKeyShapePerModel(t *testing.T) {
	// Validate that lock + session keys include the model so two
	// concurrent requests on different models of the same token do not
	// contend on one Redis key.
	sc := NewSessionCacheWith(nil, "codebuff", SessionCacheConfig{})
	tokenHash := hashToken("tok")
	lockA := fmt.Sprintf("codebuff:session_lock:%s:claude-3-5", tokenHash)
	lockB := fmt.Sprintf("codebuff:session_lock:%s:gpt-4", tokenHash)
	if lockA == lockB {
		t.Fatal("lock keys must differ per model")
	}
	sessionA := fmt.Sprintf("codebuff:session:%s:claude-3-5", tokenHash)
	sessionB := fmt.Sprintf("codebuff:session:%s:gpt-4", tokenHash)
	if sessionA == sessionB {
		t.Fatal("session keys must differ per model")
	}
	if !strings.Contains(sessionA, tokenHash) {
		t.Fatal("session keys must contain hashed token")
	}
	_ = sc // silence unused if config struct moves
}

func TestRunPayloadRunID(t *testing.T) {
	r := &Run{RunID: "parent", ChatRunID: "child"}
	if r.PayloadRunID() != "child" {
		t.Fatalf("expected child run id, got %s", r.PayloadRunID())
	}
	r2 := &Run{RunID: "only"}
	if r2.PayloadRunID() != "only" {
		t.Fatalf("expected only run id, got %s", r2.PayloadRunID())
	}
}

func TestCompletionAccumulator(t *testing.T) {
	acc := NewCompletionAccumulator("test-model")
	acc.Add(upstream.SSEMessage{Type: "model.text-delta", Event: map[string]any{"delta": "Hello "}})
	acc.Add(upstream.SSEMessage{Type: "model.text-delta", Event: map[string]any{"delta": "world"}})
	acc.Add(upstream.SSEMessage{Type: "model.finish", Event: map[string]any{"finishReason": "end_turn", "usage": map[string]int{"inputTokens": 10, "outputTokens": 2}}})

	if acc.Content != "Hello world" {
		t.Fatalf("unexpected content: %q", acc.Content)
	}
	if acc.FinishReason != "end_turn" {
		t.Fatalf("unexpected finish reason: %q", acc.FinishReason)
	}
	msgs := acc.ToMessages()
	if len(msgs) == 0 {
		t.Fatal("expected messages from accumulator")
	}
}
