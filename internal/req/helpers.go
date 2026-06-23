package req

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"orchids-api/internal/prompt"
)

var explicitEnvWorkdirRegex = regexp.MustCompile(`(?im)^\s*(?:cwd|working directory)\s*:\s*([^\n\r]+)\s*$`)
var isolatedPrimaryEnvWorkdirRegex = regexp.MustCompile(`(?im)^\s*primary\s+working\s+directory\s*:\s*([^\n\r]+)\s*$`)
var primaryEnvWorkdirRegex = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?primary\s+working\s+directory\s*:\s*([^\n\r]+)\s*$`)

func ExtractUserText(messages []prompt.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == "user" {
			if text := msg.ExtractText(); text != "" {
				return text
			}
		}
	}
	return ""
}

func ExtractWorkdir(r *http.Request, req *Request) (string, string) {
	if req.Metadata != nil {
		if wd := metadataString(req.Metadata,
			"workdir", "working_directory", "workingDirectory", "cwd",
			"workspace", "workspace_path", "workspacePath",
			"project_root", "projectRoot",
		); wd != "" {
			return strings.TrimSpace(wd), "metadata"
		}
	}

	if wd := headerValue(r,
		"X-Workdir", "X-Working-Directory", "X-Cwd", "X-Workspace", "X-Project-Root",
	); wd != "" {
		return strings.TrimSpace(wd), "header"
	}

	if wd := extractWorkdirFromSystemItems(req.System); wd != "" {
		return strings.TrimSpace(wd), "system"
	}

	if wd := extractWorkdirFromMessages(req.Messages); wd != "" {
		return strings.TrimSpace(wd), "messages"
	}

	return "", ""
}

func extractWorkdirFromSystemItems(system []SystemItem) string {
	for _, item := range system {
		if item.Type == "text" {
			text := strings.TrimSpace(item.Text)
			if text == "" {
				continue
			}
			if matches := explicitEnvWorkdirRegex.FindStringSubmatch(text); len(matches) > 1 {
				return strings.TrimSpace(matches[1])
			}
			if looksLikeClaudeEnvironmentBlock(text) {
				if wd := extractWorkdirFromEnvironmentText(text); wd != "" {
					return wd
				}
				continue
			}
			if matches := isolatedPrimaryEnvWorkdirRegex.FindStringSubmatch(text); len(matches) > 1 && countNonEmptyLines(text) <= 2 {
				return strings.TrimSpace(matches[1])
			}
		}
	}
	return ""
}

func extractWorkdirFromMessages(messages []prompt.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Content.IsString() {
			if wd := extractWorkdirFromEnvironmentText(msg.Content.GetText()); wd != "" {
				return wd
			}
			continue
		}
		for _, block := range msg.Content.GetBlocks() {
			if block.Type != "text" {
				continue
			}
			if wd := extractWorkdirFromEnvironmentText(block.Text); wd != "" {
				return wd
			}
		}
	}
	return ""
}

func extractWorkdirFromEnvironmentText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if !looksLikeClaudeEnvironmentBlock(text) {
		return ""
	}
	if matches := explicitEnvWorkdirRegex.FindStringSubmatch(text); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	if matches := primaryEnvWorkdirRegex.FindStringSubmatch(text); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	if matches := isolatedPrimaryEnvWorkdirRegex.FindStringSubmatch(text); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

var _ = countNonEmptyLines

func looksLikeClaudeEnvironmentBlock(text string) bool {
	lower := strings.ToLower(text)
	markers := 0
	for _, marker := range []string{
		"# environment",
		"primary working directory:",
		"# auto memory",
		"gitstatus:",
		"you have been invoked in the following environment",
	} {
		if strings.Contains(lower, marker) {
			markers++
		}
	}
	return markers >= 2
}

func countNonEmptyLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func ConversationKey(r *http.Request, req *Request) string {
	if req.ConversationID != "" {
		return req.ConversationID
	}
	if req.Metadata != nil {
		if key := metadataString(req.Metadata, "conversation_id", "conversationId", "session_id", "sessionId", "thread_id", "threadId", "chat_id", "chatId"); key != "" {
			return key
		}
	}
	if key := headerValue(r, "X-Conversation-Id", "X-Session-Id", "X-Thread-Id", "X-Chat-Id"); key != "" {
		return key
	}
	if req.Metadata != nil {
		if key := metadataString(req.Metadata, "user_id", "userId"); key != "" {
			return key
		}
	}
	return ""
}

func metadataString(metadata map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key]; ok {
			if str, ok := value.(string); ok {
				str = strings.TrimSpace(str)
				if str != "" {
					return str
				}
			}
		}
	}
	return ""
}

func headerValue(r *http.Request, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

type WorkdirStore interface {
	GetWorkdir(ctx context.Context, key string) (string, error)
	SetWorkdir(ctx context.Context, key, dir string) error
	Touch(ctx context.Context, key string) error
}
