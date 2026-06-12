package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"orchids-api/internal/aihubmix"
	"orchids-api/internal/config"
	"orchids-api/internal/loadbalancer"
	"orchids-api/internal/openai"
	"orchids-api/internal/store"
)

// makeAihubmixImageHandler serves POST /aihubmix/v1/images/generations.
// The body is the standard OpenAI image-generation shape; we forward to the
// aihubmix /v1/images/generations endpoint using a load-balanced account.
func makeAihubmixImageHandler(cfg *config.Config, s *store.Store, lb *loadbalancer.LoadBalancer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s == nil || lb == nil {
			http.Error(w, "store or load balancer not configured", http.StatusInternalServerError)
			return
		}

		var req openai.ImageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Prompt) == "" {
			http.Error(w, "prompt is required", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Model) == "" {
			req.Model = aihubmix.DefaultModel
		}

		// Try each enabled aihubmix account in round-robin order until one succeeds.
		excludeIDs := make([]int64, 0, 8)
		var lastErr error
		var lastStatus int
		for attempts := 0; attempts < 8; attempts++ {
			acc, err := lb.GetNextAccountExcludingByChannel(r.Context(), excludeIDs, "aihubmix")
			if err != nil {
				if attempts == 0 {
					http.Error(w, "no aihubmix accounts available: "+err.Error(), http.StatusBadGateway)
					return
				}
				break
			}
			if acc == nil {
				break
			}
			excludeIDs = append(excludeIDs, acc.ID)

			resp, status, callErr := callAihubmixImageOnce(r.Context(), cfg, acc, req)
			if callErr == nil && status >= 200 && status < 300 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
			lastErr = callErr
			lastStatus = status

			// Definitive client error (not a transient blip): surface it immediately.
			if status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests {
				break
			}

			// Transient: mark and try the next account.
			if callErr != nil {
				lb.MarkAccountStatus(r.Context(), acc, "aihubmix_image_failed")
			}
		}

		body := fmt.Sprintf(`{"error":{"message":%q,"type":"upstream_error","code":%q}}`,
			truncateForError(lastErr, 500), fmt.Sprintf("status_%d", lastStatus))
		if lastStatus == 0 {
			lastStatus = http.StatusBadGateway
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastStatus)
		_, _ = w.Write([]byte(body))
	}
}

func callAihubmixImageOnce(
	ctx context.Context,
	cfg *config.Config,
	acc *store.Account,
	req openai.ImageRequest,
) (*openai.ImageResponse, int, error) {
	apiKey := aihubmix.ResolveAPIKey(acc)
	if apiKey == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("missing aihubmix api key")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("marshal request: %w", err)
	}

	httpCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, aihubmix.BaseURL+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; orchids-api/1.0; +aihubmix)")

	timeout := 60 * time.Second
	if cfg != nil && cfg.RequestTimeout > 0 {
		timeout = time.Duration(cfg.RequestTimeout) * time.Second
		if timeout < 30*time.Second {
			timeout = 30 * time.Second
		}
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, resp.StatusCode, fmt.Errorf("aihubmix image error: %s", strings.TrimSpace(string(raw)))
	}

	var out openai.ImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("decode response: %w", err)
	}
	return &out, resp.StatusCode, nil
}

func truncateForError(err error, max int) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
