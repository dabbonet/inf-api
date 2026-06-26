package handler

import (
	"strings"

	"orchids-api/internal/tiktoken"
)

type inputTokenBreakdown struct {
	BasePromptTokens    int
	SystemContextTokens int
	HistoryTokens       int
	ToolsTokens         int
	Total               int
}

func estimateInputTokenBreakdown(promptText string, history []map[string]string, tools []interface{}) inputTokenBreakdown {
	var bd inputTokenBreakdown
	promptTokens := tiktoken.EstimateTextTokens(promptText)
	sysText := extractTaggedContent(promptText, "sys")
	if sysText == "" {
		sysText = extractTaggedContent(promptText, "system_context")
	}
	sysTokens := tiktoken.EstimateTextTokens(sysText)
	if sysTokens > promptTokens {
		sysTokens = promptTokens
	}

	bd.SystemContextTokens = sysTokens
	bd.BasePromptTokens = promptTokens - sysTokens

	for _, item := range history {
		content := strings.TrimSpace(item["content"])
		if content == "" {
			continue
		}
		bd.HistoryTokens += tiktoken.EstimateTextTokens(content) + 15
	}

	bd.ToolsTokens = estimateCompactedToolsTokens(tools)

	bd.Total = bd.BasePromptTokens + bd.SystemContextTokens + bd.HistoryTokens + bd.ToolsTokens
	return bd
}

func extractTaggedContent(text string, tag string) string {
	if text == "" || tag == "" {
		return ""
	}
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"

	start := strings.Index(text, startTag)
	if start == -1 {
		return ""
	}
	start += len(startTag)
	end := strings.Index(text[start:], endTag)
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}
