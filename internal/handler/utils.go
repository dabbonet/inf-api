package handler

import (
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"orchids-api/internal/prompt"
	"orchids-api/internal/util"
)

var explicitEnvWorkdirRegex = regexp.MustCompile(`(?im)^\s*(?:cwd|working directory)\s*:\s*([^\n\r]+)\s*$`)
var isolatedPrimaryEnvWorkdirRegex = regexp.MustCompile(`(?im)^\s*primary\s+working\s+directory\s*:\s*([^\n\r]+)\s*$`)
var primaryEnvWorkdirRegex = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?primary\s+working\s+directory\s*:\s*([^\n\r]+)\s*$`)

func extractWorkdirFromSystem(system SystemItems) string {
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

func extractWorkdirFromRequest(r *http.Request, req ClaudeRequest) (string, string) {
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

	if wd := extractWorkdirFromSystem(req.System); wd != "" {
		return strings.TrimSpace(wd), "system"
	}

	if wd := extractWorkdirFromMessages(req.Messages); wd != "" {
		return strings.TrimSpace(wd), "messages"
	}

	return "", ""
}

func channelFromPath(path string) string {
	if strings.HasPrefix(path, "/puter/") {
		return "puter"
	}
	if strings.HasPrefix(path, "/codebuff/") {
		return "codebuff"
	}
	if strings.HasPrefix(path, "/grok/") {
		return "grok"
	}
	return ""
}

// mapModel maps the requested model name to the model actually supported by the upstream.
func mapModel(requestModel string) string {
	normalized := normalizeModelKey(requestModel)
	if normalized == "" {
		return "claude-sonnet-4-6"
	}
	if mapped, ok := modelMap[normalized]; ok {
		return mapped
	}
	return "claude-sonnet-4-6"
}

func normalizeModelKey(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(normalized, "claude-") {
		normalized = strings.ReplaceAll(normalized, "4.6", "4-6")
		normalized = strings.ReplaceAll(normalized, "4.5", "4-5")
	}
	return normalized
}

var modelMap = map[string]string{
	"claude-sonnet-4-5":          "claude-sonnet-4-6",
	"claude-sonnet-4-6":          "claude-sonnet-4-6",
	"claude-sonnet-4-5-thinking": "claude-sonnet-4-5-thinking",
	"claude-sonnet-4-6-thinking": "claude-sonnet-4-6",
	"claude-opus-4-6":            "claude-opus-4-6",
	"claude-opus-4-5":            "claude-opus-4-6",
	"claude-opus-4-5-thinking":   "claude-opus-4-5-thinking",
	"claude-opus-4-6-thinking":   "claude-opus-4-6",
	"claude-haiku-4-5":           "claude-haiku-4-5",
	"claude-sonnet-4-20250514":   "claude-sonnet-4-20250514",
	"claude-3-7-sonnet-20250219": "claude-3-7-sonnet-20250219",
	"gemini-3-flash":             "gemini-3-flash",
	"gemini-3-pro":               "gemini-3-pro",
	"gpt-5.3-codex":              "gpt-5.3-codex",
	"gpt-5.2-codex":              "gpt-5.2-codex",
	"gpt-5.2":                    "gpt-5.2",
	"grok-4.1-fast":              "grok-4.1-fast",
	"glm-5":                      "glm-5",
	"kimi-k2.5":                  "kimi-k2.5",
}

func conversationKeyForRequest(r *http.Request, req ClaudeRequest) string {
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

func extractUserText(messages []prompt.Message) string {
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

func hasInterruptedRetryMarker(messages []prompt.Message) bool {
	for _, msg := range messages {
		if strings.ToLower(strings.TrimSpace(msg.Role)) != "user" {
			continue
		}
		text := strings.TrimSpace(stripSystemRemindersForMode(msg.ExtractText()))
		if strings.Contains(text, "[Request interrupted by user]") {
			return true
		}
	}
	return false
}

func lastUserIsToolResultFollowup(messages []prompt.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		if msg.Content.IsString() {
			return false
		}
		blocks := msg.Content.GetBlocks()
		hasToolResult := false
		for _, block := range blocks {
			switch block.Type {
			case "tool_result":
				hasToolResult = true
			case "text":
				continue
			default:
				if strings.TrimSpace(block.Type) != "" {
					return false
				}
			}
		}
		return hasToolResult
	}
	return false
}

func extractToolResultContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		return util.NormalizePersistedToolResultText(v)
	case []interface{}:
		var parts []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = util.NormalizePersistedToolResultText(s)
				if s != "" {
					parts = append(parts, s)
				}
			}
		}
		return util.NormalizePersistedToolResultText(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func lastNonToolResultUserText(messages []prompt.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			continue
		}
		if msg.Content.IsString() {
			text := strings.TrimSpace(stripSystemRemindersForMode(msg.Content.GetText()))
			if text != "" && !containsSuggestionMode(text) {
				return text
			}
			continue
		}
		blocks := msg.Content.GetBlocks()
		var parts []string
		hasToolResult := false
		for _, block := range blocks {
			switch block.Type {
			case "tool_result":
				hasToolResult = true
			case "text":
				text := strings.TrimSpace(stripSystemRemindersForMode(block.Text))
				if text != "" && !containsSuggestionMode(text) {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}
		if hasToolResult {
			continue
		}
	}
	return ""
}

func looksLikeToolResultFailure(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"file does not exist",
		"no such file or directory",
		"no such file",
		"cannot find the file",
		"cannot open file",
		"permission denied",
		"is a directory",
		"current working directory is ",
		"file has not been read yet",
		"read it first before writing to it",
		"old_string not found",
		"string to replace not found",
		"could not find old_string",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isSuggestionMode(messages []prompt.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == "user" {
			text := msg.ExtractText()
			if text != "" {
				return containsSuggestionMode(text)
			}
			return false
		}
	}
	return false
}

func buildLocalSuggestion(messages []prompt.Message) string {
	lastUser := lastNonSuggestionUserText(messages)
	lastAssistant := lastAssistantText(messages)
	if lastAssistant == "" {
		return ""
	}
	if !hasExplicitNextStepOffer(lastAssistant) {
		return ""
	}
	if containsHan(lastUser) || containsHan(lastAssistant) {
		return "Okay"
	}
	return "go ahead"
}

func containsSuggestionMode(text string) bool {
	clean := stripSystemRemindersForMode(text)
	return strings.Contains(strings.ToLower(clean), "suggestion mode")
}

func lastNonSuggestionUserText(messages []prompt.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			continue
		}
		text := strings.TrimSpace(stripSystemRemindersForMode(msg.ExtractText()))
		if text == "" || containsSuggestionMode(text) {
			continue
		}
		return text
	}
	return ""
}

func buildToolGateMessage(messages []prompt.Message, suggestionMode bool) string {
	if suggestionMode {
		return "This is a suggestion-mode follow-up. Answer directly without calling tools or performing any file operations."
	}
	if lastUserIsToolResultFollowup(messages) {
		original := lastNonToolResultUserText(messages)
		if looksLikeOptimizationRequest(original) {
			return "Use the provided tool results to answer the user's project optimization request directly. Tool access is unavailable for this turn, and any request to read, inspect, search, or review more files will be ignored. Stay specific to the current project and available code context. Do NOT call tools, do not describe a plan, and do not say you will first analyze or review the codebase. Give the best concrete project-specific recommendations now."
		}
		return "Use the provided tool results to answer the user's follow-up directly. Tool access is unavailable for this turn, and any request to read, inspect, search, or review more files will be ignored. Stay specific to the current project and available code context. Do NOT call tools, do not describe a plan, and answer now based only on the provided results."
	}
	return "Answer directly without calling tools or performing any file operations."
}

func lastAssistantText(messages []prompt.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			continue
		}
		text := strings.TrimSpace(stripSystemRemindersForMode(msg.ExtractText()))
		if text != "" {
			return text
		}
	}
	return ""
}

func looksLikeExploratoryAssistantPreface(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if len([]rune(text)) > 260 {
		return false
	}

	lower := strings.ToLower(strings.Join(strings.Fields(text), " "))
	intro := []string{
		"let me",
		"i'll first",
		"i will first",
		"let me first",
		"I first",
	}
	action := []string{
		"look",
		"read",
		"explore",
		"examine",
		"analyze",
		"identify",
		"understand",
		"inspect",
		"review",
		"check",
		"learn",
		"have a look",
		"Take a look",
		"learn",
		"read",
		"read",
		"understand",
		"analyze",
		"examine",
		"review",
	}

	hasIntro := false
	for _, marker := range intro {
		if strings.Contains(lower, marker) {
			hasIntro = true
			break
		}
	}
	if !hasIntro {
		return false
	}
	for _, marker := range action {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasExplicitNextStepOffer(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	englishMarkers := []string{
		"if you want",
		"if you'd like",
		"if you need",
		"i can continue",
		"i can also",
		"i can help",
		"i can restart",
		"i can check",
		"i can review",
		"i can commit",
		"i can push",
	}
	for _, marker := range englishMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	chineseMarkers := []string{
		"if you want",
		"if needed",
		"if you want",
		"If you want",
		"if necessary",
		"I'm Okay to continue",
		"I'm Okay directly",
		"I can help you",
		"My next step Okay",
	}
	for _, marker := range chineseMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func containsHan(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

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

func looksLikeTechStackRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"technology architecture", "tech stack", "architecture", "framework", "dependencies", "what technologies are used", "make what technologies are used",
		"tech stack", "technology stack", "architecture", "framework", "frameworks", "dependencies",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeProjectPurposeRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"what is this project for", "what is this project for", "what does this project do", "what is the use of this project", "purpose", "do what",
		"what does this project do", "what is this project", "project purpose", "purpose of this project",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeBackendImplementationRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"backend", "server side", "api", "api", "backendhow to implement", "apihow to implement",
		"backend", "server side", "server-side", "api implementation", "service implementation",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeDataLayerRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"data layer", "storage", "database", "cache", "persistence", "how is data stored", "where is data stored",
		"data layer", "storage", "database", "db", "cache", "persistence",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeTestingRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"test", "unit test", "Integrated test", "e2e", "How to test", "How to test",
		"testing", "test strategy", "unit test", "integration test", "e2e test",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeDeploymentRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"deploy", "build", "publish", "go online", "run method", "how to start",
		"deployment", "deploy", "build", "release", "runtime", "how to run", "how to start",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeSecurityRiskRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"security risk", "security issue", "vulnerability", "risk point", "insecure", "security hidden danger",
		"security risk", "security risks", "security issue", "vulnerability", "vulnerabilities",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikePermissionRiskRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"permission risk", "permission issue", "privilege escalation", "root permission", "high permission", "least privilege",
		"permission risk", "permissions issue", "privilege escalation", "run as root", "least privilege",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeDependencyRiskRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"dependenciesrisk", "dependencies problem", "supply chain risk", "Are dependencies safe?", "Third-party dependencies risk",
		"dependency risk", "dependency risks", "package risk", "supply chain risk", "third-party dependency",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeReleaseRiskRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"publish risk", "publishing risk", "go online risk", "delivery risk", "publish hidden dangers", "publish problem",
		"release risk", "rollout risk", "deployment risk", "shipping risk", "release issue",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeOptimizationRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"how to optimize", "how to optimize", "optimization suggestions", "performancehow to optimize", "Recommend", "improvement suggestions",
		"help me optimize", "optimize this project", "project optimization", "optimize this project a bit", "help me improve this project",
		"optimize this solution", "help me optimize this solution", "optimize this design", "help me optimize this design", "optimize this implementation", "help me optimize this implementation",
		"how to optimize", "optimization advice", "performance optimization", "refactor suggestions", "improvement suggestions",
		"optimize this plan", "optimize this design", "optimize this implementation",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func explicitlyRequestsDeepAnalysis(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"in-depth", "detailed analysis", "deep analysis", "comprehensive analysis",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeWebImplementationRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripSystemRemindersForMode(text)))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"web page", "frontend", "page", "interface", "website", "web ui", "web-ui", "how to implement",
		"frontend", "front-end", "web", "page", "pages", "website",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isTopicClassifierRequest(req ClaudeRequest) bool {
	for _, item := range req.System {
		if strings.ToLower(strings.TrimSpace(item.Type)) != "text" {
			continue
		}
		lower := strings.ToLower(stripSystemRemindersForMode(item.Text))
		if strings.Contains(lower, "new conversation topic") &&
			strings.Contains(lower, "isnewtopic") &&
			strings.Contains(lower, "json object") &&
			strings.Contains(lower, "title") {
			return true
		}
	}
	return false
}

func isTitleGenerationRequest(req ClaudeRequest) bool {
	hasTitleInstruction := false
	hasJSONInstruction := false

	for _, item := range req.System {
		if strings.ToLower(strings.TrimSpace(item.Type)) != "text" {
			continue
		}
		lower := strings.ToLower(stripSystemRemindersForMode(item.Text))
		if strings.Contains(lower, "generate a concise, sentence-case title") ||
			(strings.Contains(lower, "sentence-case title") && strings.Contains(lower, "coding session")) {
			hasTitleInstruction = true
		}
		if strings.Contains(lower, "return json with a single \"title\" field") ||
			(strings.Contains(lower, "return json") && strings.Contains(lower, "single") && strings.Contains(lower, "\"title\"")) {
			hasJSONInstruction = true
		}
	}

	return hasTitleInstruction && hasJSONInstruction
}

func classifyTopicRequest(req ClaudeRequest) (bool, string) {
	userTexts := extractUserTexts(req.Messages)
	if len(userTexts) == 0 {
		return false, ""
	}

	latest := strings.TrimSpace(userTexts[len(userTexts)-1])
	if latest == "" {
		return false, ""
	}

	prev := ""
	if len(userTexts) >= 2 {
		prev = strings.TrimSpace(userTexts[len(userTexts)-2])
	}

	if prev == "" {
		return true, generateTopicTitle(latest)
	}

	if isGreetingText(latest) {
		return false, ""
	}

	latestNorm := normalizeTopicText(latest)
	prevNorm := normalizeTopicText(prev)
	if latestNorm == "" || prevNorm == "" {
		return latest != prev, generateTopicTitle(latest)
	}
	if latestNorm == prevNorm || strings.Contains(latestNorm, prevNorm) || strings.Contains(prevNorm, latestNorm) {
		return false, ""
	}
	return true, generateTopicTitle(latest)
}

func extractUserTexts(messages []prompt.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, msg := range messages {
		if strings.ToLower(strings.TrimSpace(msg.Role)) != "user" {
			continue
		}
		text := strings.TrimSpace(stripSystemRemindersForMode(msg.ExtractText()))
		if text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func isGreetingText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	switch lower {
	case "hi", "hello", "hey", "are you there":
		return true
	default:
		return false
	}
}

func normalizeTopicText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func generateTopicTitle(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "New Topic"
	}
	words := strings.Fields(trimmed)
	if len(words) >= 2 {
		if len(words) > 3 {
			words = words[:3]
		}
		return strings.Join(words, " ")
	}
	runes := []rune(trimmed)
	if len(runes) > 10 {
		runes = runes[:10]
	}
	return strings.TrimSpace(string(runes))
}

// stripSystemRemindersForMode removes <system-reminder>...</system-reminder> to avoid misjudgment of plan/suggestion mode
// Use LastIndex to find closing tags and handle nested literal tags correctly
func stripSystemRemindersForMode(text string) string {
	text = stripNestedModeTaggedBlock(text, "system-reminder")
	for _, tag := range []string{
		"local-command-caveat",
		"command-name",
		"command-message",
		"command-args",
		"local-command-stdout",
		"local-command-stderr",
		"local-command-exit-code",
	} {
		text = stripSimpleModeTaggedBlock(text, tag)
	}
	return text
}

func stripNestedModeTaggedBlock(text string, tag string) string {
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"
	if !strings.Contains(text, startTag) {
		return text
	}
	var sb strings.Builder
	sb.Grow(len(text))
	i := 0
	for i < len(text) {
		start := strings.Index(text[i:], startTag)
		if start == -1 {
			sb.WriteString(text[i:])
			break
		}
		sb.WriteString(text[i : i+start])
		blockStart := i + start
		endStart := blockStart + len(startTag)
		end := strings.LastIndex(text[endStart:], endTag)
		if end == -1 {
			sb.WriteString(text[blockStart:])
			break
		}
		i = endStart + end + len(endTag)
	}
	return sb.String()
}

func stripSimpleModeTaggedBlock(text string, tag string) string {
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"
	if !strings.Contains(text, startTag) {
		return text
	}
	var sb strings.Builder
	sb.Grow(len(text))
	i := 0
	for i < len(text) {
		start := strings.Index(text[i:], startTag)
		if start == -1 {
			sb.WriteString(text[i:])
			break
		}
		sb.WriteString(text[i : i+start])
		blockStart := i + start
		endStart := blockStart + len(startTag)
		end := strings.Index(text[endStart:], endTag)
		if end == -1 {
			sb.WriteString(text[blockStart:])
			break
		}
		i = endStart + end + len(endTag)
	}
	return sb.String()
}
