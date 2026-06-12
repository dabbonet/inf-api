package handler

import (
	"fmt"
	"strings"

	"orchids-api/internal/prompt"
)

// resetMessagesForNewWorkdir retains the current user messages when the work directory is switched.
// And compress the previous conversation history into a summary and inject it into the message to avoid completely losing context.
func resetMessagesForNewWorkdir(messages []prompt.Message) []prompt.Message {
	if len(messages) == 0 {
		return messages
	}

	// Find the last user message
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "user") {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return []prompt.Message{}
	}

	// If there is no historical message that needs summary, return directly
	if lastUserIdx == 0 {
		return []prompt.Message{messages[lastUserIdx]}
	}

	// build short summary
	older := messages[:lastUserIdx]
	summary := buildWorkdirChangeSummary(older)

	if summary == "" {
		return []prompt.Message{messages[lastUserIdx]}
	}

	// Inject the summary as a user message to let the model know the previous context
	summaryMsg := prompt.Message{
		Role:    "user",
		Content: prompt.MessageContent{Text: fmt.Sprintf("[Previous conversation summary before working directory change]\n%s", summary)},
	}
	return []prompt.Message{summaryMsg, messages[lastUserIdx]}
}

// buildWorkdirChangeSummary Extracts key contextual summaries from historical messages.
func buildWorkdirChangeSummary(messages []prompt.Message) string {
	if len(messages) == 0 {
		return ""
	}

	var parts []string
	for _, msg := range messages {
		text := msg.ExtractText()
		if text == "" {
			continue
		}
		// Truncate a single message that is too long
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		parts = append(parts, fmt.Sprintf("- %s: %s", msg.Role, text))
	}

	if len(parts) == 0 {
		return ""
	}

	// Limit the total number of abstracts to avoid being too long
	if len(parts) > 10 {
		parts = parts[len(parts)-10:]
	}

	return strings.Join(parts, "\n")
}


