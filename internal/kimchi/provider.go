package kimchi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"

	"orchids-api/internal/config"
	"orchids-api/internal/debug"
	"orchids-api/internal/store"
	"orchids-api/internal/upstream"
)

// Provider implements upstream.UpstreamClient for Kimchi's "Serverless
// Inference" gateway (https://llm.kimchi.dev). It is registered for the
// handler.handlePassthroughProvider fast-path via the provider registry:
//
//	h.RegisterSpec(kimchiprov.Spec()) // implements provider.Spec with
//	                                 // ClientFactory -> kimchi.NewFromAccount
//
// Wire contract:
//
//   - We receive the original client request body in req.RawBody — UTF-8 JSON.
//     The handler parses only model + stream + messages + system. We send
//     RawBody upstream byte-for-byte; zero translation.
//   - We return whatever upstream sends back, via req.RawSSEWriter when
//     streaming, or via a single-shot Writer call for non-stream JSON bodies.
//
// Auth model: a single bearer token per Account (no refresh, no rotation).
// ResolveAuthToken picks from the 4 store credential fields.

type Provider struct {
	client  *Client
	config  *config.Config
	account *store.Account
}

// NewFromAccount builds a Provider from a stored Account. Mirrors the
// provider.Spec.ClientFactory signature expected by handler.go.
func NewFromAccount(acc *store.Account, cfg *config.Config) *Provider {
	token := ResolveAuthToken(acc)
	if token == "" {
		return nil
	}
	return &Provider{
		client:  NewClient(token, cfg),
		config:  cfg,
		account: acc,
	}
}

// SendRequestWithPayload is the entrypoint called by the inf-api handler.
//
// We don't use req.Prompt / req.Messages / req.Tools (passthrough mode).
// We use req.RawBody, req.RawSSEWriter, req.Stream.
func (p *Provider) SendRequestWithPayload(
	ctx context.Context,
	req upstream.UpstreamRequest,
	_ func(upstream.SSEMessage),
	logger *debug.Logger,
) error {
	if p == nil || p.client == nil {
		return errors.New("kimchi: provider not initialized")
	}
	if len(req.RawBody) == 0 {
		return errors.New("kimchi: empty RawBody on passthrough request")
	}

	kind := guessKind(req.RawBody)
	if logger != nil {
		logger.LogUpstreamRequest(p.client.upstreamURL(kind), map[string]string{"channel": "kimchi"}, nil)
	}

	if req.Stream {
		return p.streamUpstream(ctx, kind, req)
	}
	return p.completeUpstream(ctx, kind, req)
}

// streamUpstream forwards the SSE stream from upstream to the client.
func (p *Provider) streamUpstream(ctx context.Context, kind endpointKind, req upstream.UpstreamRequest) error {
	write := req.RawSSEWriter
	if write == nil {
		// Defensive: handler should always allocate this for streaming paths.
		// If we got here without it, silently discard upstream bytes — the
		// session is broken at the dispatcher level, not our problem.
		write = func(_ string, _ []byte) {}
	}
	if err := p.client.SendStream(ctx, kind, req.RawBody, write); err != nil {
		slog.Warn("kimchi streaming send failed",
			"model", req.Model,
			"endpoint", endpointName(kind),
			"error", err)
		return err
	}
	return nil
}

// completeUpstream sends a non-stream request and emits the JSON response.
// In passthrough mode non-stream is rare, but the contract requires us to
// support it (e.g. /v1/messages without stream:true, JSON-only clients).
// We emit a single SSE-shaped event with the body and a [DONE] marker so
// downstream handler logic stays uniform. If RawSSEWriter isn't set, this
// is a degenerate path and we just log + return.
func (p *Provider) completeUpstream(ctx context.Context, kind endpointKind, req upstream.UpstreamRequest) error {
	body, httpErr, err := p.client.SendJSON(ctx, kind, req.RawBody)
	if err != nil {
		return err
	}
	if httpErr != nil {
		return httpErr
	}
	if len(body) == 0 {
		return errors.New("kimchi: empty response body on non-stream request")
	}
	if req.RawSSEWriter != nil {
		req.RawSSEWriter("body", body)
	}
	return nil
}

// guessKind peeks the request body JSON to pick between the OpenAI and
// Anthropic upstream surfaces. The canonical heuristic:
//
//   - Anthropic Messages bodies typically carry `"system":[ {type:text,...} ]`
//     (system is an array of structured blocks).
//   - OpenAI Chat Completions bodies usually omit "system" or carry it as a
//     single string.
//
// Default is OpenAI because that's the more common caller shape, and the
// upstream is forgiving when the body is OpenAI-shaped.
func guessKind(rawBody []byte) endpointKind {
	if bytes.Contains(rawBody, []byte(`"system":[`)) {
		return endpointAnthropic
	}
	if bytes.Contains(rawBody, []byte(`"anthropic_version"`)) {
		return endpointAnthropic
	}
	return endpointOpenAI
}

func endpointName(k endpointKind) string {
	switch k {
	case endpointAnthropic:
		return "anthropic"
	default:
		return "openai"
	}
}

// httpErrorString is a small helper for tests / logs.
func httpErrorString(h *HTTPError) string {
	if h == nil {
		return ""
	}
	return strings.TrimSpace(h.Body)
}

// Compile-time guard: *Provider satisfies upstream.UpstreamClient.
var _ upstream.UpstreamClient = (*Provider)(nil)
