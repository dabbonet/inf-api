package handler

import (
	"log/slog"
	"net/http"
	"time"

	"orchids-api/internal/adapter"
	"orchids-api/internal/debug"
	"orchids-api/internal/provider"
	"orchids-api/internal/store"
	apperq "orchids-api/internal/req"
)

// runPreWorkdirFastPaths handles local-only dispatches that don't need the
// resolved working directory: command-prefix, topic-classifier, and
// title-generation. Returns true if one of them fired.
func (h *Handler) runPreWorkdirFastPaths(
	w http.ResponseWriter,
	r *http.Request,
	req *ClaudeRequest,
	responseFormat adapter.ResponseFormat,
	startTime time.Time,
	logger *debug.Logger,
	verboseDiagnostics bool,
) bool {
	if ok, command := isCommandPrefixRequest(*req); ok {
		if verboseDiagnostics {
			slog.Debug("Handling command prefix request", "command", command)
		}
		prefix := detectCommandPrefix(command)
		logger.LogEarlyExit("command_prefix", map[string]interface{}{
			"command": command,
			"prefix":  prefix,
		})
		writeCommandPrefixResponse(w, *req, responseFormat, prefix, startTime, logger)
		return true
	}

	if isTopicClassifierRequest(*req) {
		if verboseDiagnostics {
			slog.Debug("Handling topic classifier request locally")
		}
		logger.LogEarlyExit("topic_classifier", map[string]interface{}{
			"mode": "local",
		})
		writeTopicClassifierResponse(w, *req, responseFormat, startTime, logger)
		return true
	}

	if isTitleGenerationRequest(*req) {
		title := generateTopicTitle(extractUserText(req.Messages))
		if verboseDiagnostics {
			slog.Debug("Handling title generation request locally", "title", title)
		}
		logger.LogEarlyExit("title_generation", map[string]interface{}{
			"mode":  "local",
			"title": title,
		})
		writeTitleGenerationResponse(w, *req, responseFormat, startTime, logger)
		return true
	}

	return false
}

// runPostWorkdirFastPaths handles local-only dispatches that need the
// resolved working directory: current-workdir probes and suggestion-mode.
// Suggestion-mode runs last so it sees the same request shape as the
// upstream path. Returns true if one of them fired.
func (h *Handler) runPostWorkdirFastPaths(
	w http.ResponseWriter,
	r *http.Request,
	req *ClaudeRequest,
	responseFormat adapter.ResponseFormat,
	effectiveWorkdir string,
	startTime time.Time,
	logger *debug.Logger,
	verboseDiagnostics bool,
) bool {
	if isCurrentWorkdirRequest(*req) {
		logger.LogEarlyExit("current_workdir", map[string]interface{}{
			"mode":    "local",
			"workdir": effectiveWorkdir,
			"path":    r.URL.Path,
		})
		writeCurrentWorkdirResponse(w, *req, responseFormat, effectiveWorkdir, startTime, logger)
		return true
	}

	if isSuggestionMode(req.Messages) {
		suggestion := buildLocalSuggestion(req.Messages)
		if verboseDiagnostics {
			slog.Debug("Handling suggestion mode request locally", "suggestion", suggestion)
		}
		logger.LogEarlyExit("suggestion_mode", map[string]interface{}{
			"mode":       "local",
			"suggestion": suggestion,
		})
		writeSuggestionModeResponse(w, *req, responseFormat, startTime, logger)
		return true
	}

	return false
}

// resolveSpecForRequest returns the registered Spec for the URL or target
// channel and its ModeOptions that drive all per-provider behaviour. When
// no spec matches, zero values are returned.
func (h *Handler) resolveSpecForRequest(
	r *http.Request,
	targetChannel string,
) (provider.Spec, provider.ModeOptions) {
	var spec provider.Spec
	if s, ok := h.ResolveSpec(r, targetChannel); ok {
		spec = s
	}
	return spec, spec.Mode
}

// resolveSpecForAccount re-resolves a spec from the chosen account's
// AccountType when it differs from the URL-derived spec. The load balancer
// can hand back a puter account even when the URL prefix is "" — Mode must
// follow the account actually used, not the path.
func (h *Handler) resolveSpecForAccount(
	currentAccount *store.Account,
	currentSpec provider.Spec,
) (provider.Spec, provider.ModeOptions) {
	if currentAccount == nil {
		return currentSpec, currentSpec.Mode
	}
	if currentSpec.Name == "" || currentSpec.Name == currentAccount.AccountType {
		return currentSpec, currentSpec.Mode
	}
	if s, ok := h.SpecByName(currentAccount.AccountType); ok {
		return s, s.Mode
	}
	return currentSpec, currentSpec.Mode
}

// applySpecSanitization runs the system/messages stripping appropriate for
// the resolved spec.
//
// Default behaviour: drop Claude Code development artifacts from system
// items.
//
// SkipDefaultSanitize=true: keep tooling descriptions intact, then strip
// embedded <system-reminder> from message blocks (puter-style).
func (h *Handler) applySpecSanitization(
	req *ClaudeRequest,
	spec provider.Spec,
	mode provider.ModeOptions,
	verboseDiagnostics bool,
) {
	if !mode.SkipDefaultSanitize {
		if verboseDiagnostics {
			slog.Debug("Checkpoint: skip context trimming")
		}
		if err := apperq.SanitizeSystemItems(h.config)(req); err != nil {
			slog.Warn("Failed to sanitize system items", "error", err)
		}
		return
	}

	if verboseDiagnostics {
		slog.Debug("Checkpoint: passthrough mode, skip default sanitize", "spec", spec.Name)
	}
	if err := apperq.SanitizeSystemItemsPuter(h.config)(req); err != nil {
		slog.Warn("Failed to sanitize puter-mode system items", "error", err)
	} else if verboseDiagnostics {
		slog.Debug("puter-mode: sanitized forwarded system items", "spec", spec.Name)
	}
	req.Messages = SanitizePuterMessages(req.Messages)
}

// buildPromptProfile derives the prompt-building profile name from a spec.
// Returns "generic" when the spec did not specify one.
func buildPromptProfile(mode provider.ModeOptions) string {
	if mode.PromptProfile != "" {
		return mode.PromptProfile
	}
	return "generic"
}

// mapModelForSpec picks the upstream model name. By default the model is
// passed through mapModel(); spec.UseRawModel=true skips normalization.
func mapModelForSpec(reqModel string, mode provider.ModeOptions) string {
	if mode.UseRawModel {
		return reqModel
	}
	return mapModel(reqModel)
}