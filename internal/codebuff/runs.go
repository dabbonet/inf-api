package codebuff

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// AdChain handles the gravity → zeroclick ad sequence.  Errors are logged
// but never returned to the caller — chat proceeds regardless.
type AdChain struct {
	client    *Client
	providers []string
}

// NewAdChain creates an AdChain with the configured providers.
func NewAdChain(client *Client, providers []string) *AdChain {
	if len(providers) == 0 {
		providers = []string{"gravity", "zeroclick"}
	}
	return &AdChain{client: client, providers: providers}
}

// Request fetches ads from each provider in order until one succeeds.
// It then reports impressions to zeroclick and codebuff.
func (ac *AdChain) Request(ctx context.Context, messages []map[string]any, surface string) {
	for _, provider := range ac.providers {
		ad, err := ac.tryProvider(ctx, provider, messages, surface)
		if err != nil {
			slog.Warn("ads provider failed; continuing without blocking chat", "provider", provider, "error", err)
			continue
		}
		if ad == nil {
			continue
		}
		// Report impressions concurrently (best-effort).
		go ac.reportImpressions(ctx, ad)
		return
	}
	slog.Debug("all ad providers exhausted; proceeding without ads")
}

func (ac *AdChain) tryProvider(ctx context.Context, provider string, messages []map[string]any, surface string) (map[string]any, error) {
	data, err := ac.client.RequestAds(ctx, provider, messages, surface)
	if err != nil {
		return nil, err
	}
	ads, ok := data["ads"].([]any)
	if !ok || len(ads) == 0 {
		return nil, nil
	}
	ad, ok := ads[0].(map[string]any)
	if !ok {
		return nil, nil
	}
	return ad, nil
}

func (ac *AdChain) reportImpressions(ctx context.Context, ad map[string]any) {
	var ids []string
	if raw, ok := ad["impressionIds"].([]any); ok {
		for _, id := range raw {
			if s, ok := id.(string); ok {
				ids = append(ids, s)
			}
		}
	}
	impURL, _ := ad["impUrl"].(string)

	// Use a fresh timeout so the main request isn't held up.
	reportCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := ac.client.ReportZeroclickImpressions(reportCtx, ids); err != nil {
		slog.Debug("zeroclick impression report failed", "error", err)
	}
	if err := ac.client.ReportCodebuffImpression(reportCtx, impURL); err != nil {
		slog.Debug("codebuff impression report failed", "error", err)
	}
}

// ---------------------------------------------------------------------------
// Run bookkeeping
// ---------------------------------------------------------------------------

// Run holds the IDs created by the run chain.
type Run struct {
	RunID         string
	AgentID       string
	StartedAt     string
	ChildRunID    string
	ChatRunID     string
	ChatStartedAt string
}

// PayloadRunID returns the run ID to embed in the upstream chat payload.
func (r *Run) PayloadRunID() string {
	if r.ChatRunID != "" {
		return r.ChatRunID
	}
	return r.RunID
}

// StartRunChain creates the parent/child run structure required by codebuff.
func StartRunChain(ctx context.Context, client *Client, model *ModelConfig) (*Run, error) {
	startedAt := utcNowISO()
	if model.ParentAgentID != "" {
		return startChildChatRunChain(ctx, client, model, startedAt)
	}
	return startStandardRunChain(ctx, client, model, startedAt)
}

func startStandardRunChain(ctx context.Context, client *Client, model *ModelConfig, startedAt string) (*Run, error) {
	runID, err := client.StartRun(ctx, model.AgentID, []string{})
	if err != nil {
		return nil, fmt.Errorf("start parent run: %w", err)
	}
	childStartedAt := utcNowISO()
	childRunID, err := client.StartRun(ctx, ContextPrunerAgentID, []string{runID})
	if err != nil {
		return nil, fmt.Errorf("start child run: %w", err)
	}

	// Fire background bookkeeping (best-effort).
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = client.RecordRunStep(bgCtx, childRunID, 1, nil, "", childStartedAt)
		_ = client.FinishRun(bgCtx, childRunID, 2)
		_ = client.RecordRunStep(bgCtx, runID, 1, []string{childRunID}, "", startedAt)
	}()

	return &Run{
		RunID:      runID,
		AgentID:    model.AgentID,
		StartedAt:  startedAt,
		ChildRunID: childRunID,
	}, nil
}

func startChildChatRunChain(ctx context.Context, client *Client, model *ModelConfig, startedAt string) (*Run, error) {
	parentRunID, err := client.StartRun(ctx, model.ParentAgentID, []string{})
	if err != nil {
		return nil, fmt.Errorf("start parent run: %w", err)
	}
	chatStartedAt := utcNowISO()
	chatRunID, err := client.StartRun(ctx, model.AgentID, []string{parentRunID})
	if err != nil {
		return nil, fmt.Errorf("start chat run: %w", err)
	}
	return &Run{
		RunID:         parentRunID,
		AgentID:       model.ParentAgentID,
		StartedAt:     startedAt,
		ChildRunID:    chatRunID,
		ChatRunID:     chatRunID,
		ChatStartedAt: chatStartedAt,
	}, nil
}

// FinalizeRun records steps and finishes the run chain.  Errors are swallowed.
func FinalizeRun(ctx context.Context, client *Client, run *Run, messageID string) {
	if run == nil || client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if run.ChatRunID != "" && run.ChatRunID != run.RunID {
		// Parent/child pattern (Gemini variants).
		_ = client.RecordRunStep(ctx, run.ChatRunID, 1, nil, messageID, run.ChatStartedAt)
		_ = client.FinishRun(ctx, run.ChatRunID, 2)
		_ = client.RecordRunStep(ctx, run.RunID, 1, []string{run.ChatRunID}, "", run.StartedAt)
		_ = client.FinishRun(ctx, run.RunID, 2)
		return
	}

	// Standard pattern.
	_ = client.RecordRunStep(ctx, run.RunID, 2, nil, messageID, run.StartedAt)
	_ = client.FinishRun(ctx, run.RunID, 3)
}

func utcNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
