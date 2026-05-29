package warp

import (
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-json"
	v1 "github.com/warpdotdev/warp-proto-apis/apis/multi_agent/v1/gen/go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"orchids-api/internal/orchids"
	"orchids-api/internal/prompt"
	"orchids-api/internal/tiktoken"
	"orchids-api/internal/upstream"
)

type InputTokenEstimate struct {
	Profile          string
	QueryTokens      int
	BasePromptTokens int
	HistoryTokens    int
	ToolResultTokens int
	ToolSchemaTokens int
	ToolCount        int
	Total            int
}

type promptBuild struct {
	Full            string
	Query           string
	BasePrompt      string
	HistoryText     string
	ToolResultsText string
}

type renderedBlock struct {
	role         string
	text         string
	isToolResult bool
}

func buildRequestBytes(req upstream.UpstreamRequest) (string, []byte, error) {
	built := buildPrompt(req.Prompt, req.Messages, req.System, req.Tools, req.NoTools, req.Workdir)
	if strings.TrimSpace(built.Full) == "" {
		return "", nil, fmt.Errorf("empty warp prompt")
	}

	disableWarpTools := req.NoTools || len(req.Tools) == 0
	payload, err := buildRequestBytesFromOfficialProto(
		built.Full,
		normalizeWarpTemplateModel(req.Model),
		disableWarpTools,
		req.Workdir,
		req.ChatSessionID,
		req.Tools,
	)
	if err != nil {
		return "", nil, err
	}

	return built.Full, payload, nil
}

func buildPrompt(promptText string, messages []prompt.Message, systemItems []prompt.SystemItem, _ []interface{}, _ bool, _ string) promptBuild {
	systemText := buildSystemText(systemItems, messages)
	rendered, queryText := renderConversation(messages, promptText)

	var conversation strings.Builder
	var historyText strings.Builder
	var toolResultsText strings.Builder
	for _, block := range rendered {
		section := renderConversationBlock(block)
		conversation.WriteString(section)
		if block.isToolResult {
			toolResultsText.WriteString(section)
		} else {
			historyText.WriteString(section)
		}
	}

	basePrompt := systemText + "<|conversation|>\n"

	var full strings.Builder
	full.WriteString(basePrompt)
	full.WriteString(conversation.String())
	full.WriteString("<|end_conversation|>\n")
	full.WriteString("<|assistant|>\n")

	return promptBuild{
		Full:            full.String(),
		Query:           queryText,
		BasePrompt:      basePrompt,
		HistoryText:     historyText.String(),
		ToolResultsText: toolResultsText.String(),
	}
}

func buildSystemText(systemItems []prompt.SystemItem, messages []prompt.Message) string {
	var custom []string
	for _, item := range systemItems {
		if text := sanitizeUTF8(strings.TrimSpace(item.Text)); text != "" {
			custom = append(custom, text)
		}
	}
	for _, msg := range messages {
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "system") {
			continue
		}
		if text := extractMessageText(msg.Content); text != "" {
			custom = append(custom, text)
		}
	}

	var b strings.Builder
	b.WriteString("<|system_prompt|>\n")
	b.WriteString("You are an AI assistant integrated into Warp terminal. ")
	b.WriteString("You help users with coding tasks, terminal commands, and software development. ")
	b.WriteString("Follow the user's instructions carefully and provide helpful, accurate responses.\n")
	b.WriteString("<|end_system_prompt|>\n")

	for _, text := range custom {
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteByte('\n')
		}
	}

	b.WriteString("<|agent_mode|>\n")
	b.WriteString("You have access to tools. Use them when needed.\n")
	b.WriteString("Format your responses clearly. Use markdown for code blocks. ")
	b.WriteString("When executing commands, show the command and explain the output. ")
	b.WriteString("Be concise but thorough.\n")
	b.WriteString("Do not execute destructive commands without confirmation. ")
	b.WriteString("Do not access or modify files outside the user's workspace. ")
	b.WriteString("Respect the user's privacy and do not share sensitive information.\n")

	return b.String()
}

func renderConversation(messages []prompt.Message, promptText string) ([]renderedBlock, string) {
	if len(messages) == 0 {
		promptText = strings.TrimSpace(promptText)
		if promptText == "" {
			return nil, ""
		}
		promptText = sanitizeUTF8(promptText)
		return []renderedBlock{{role: "user", text: promptText}}, promptText
	}

	var out []renderedBlock
	lastUserText := ""
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" || role == "system" {
			continue
		}

		text, toolResults := renderMessageContent(msg.Content)
		if text != "" {
			out = append(out, renderedBlock{role: role, text: text})
			if role == "user" {
				lastUserText = text
			}
		}
		for _, result := range toolResults {
			out = append(out, renderedBlock{role: "tool", text: result, isToolResult: true})
		}
	}

	if len(out) == 0 && strings.TrimSpace(promptText) != "" {
		text := sanitizeUTF8(strings.TrimSpace(promptText))
		out = append(out, renderedBlock{role: "user", text: text})
		lastUserText = text
	}

	return out, lastUserText
}

func renderMessageContent(content prompt.MessageContent) (string, []string) {
	if content.IsString() {
		return sanitizeUTF8(strings.TrimSpace(content.GetText())), nil
	}

	var textParts []string
	var toolResults []string
	for _, block := range content.GetBlocks() {
		switch block.Type {
		case "text":
			if text := sanitizeUTF8(strings.TrimSpace(block.Text)); text != "" {
				textParts = append(textParts, text)
			}
		case "tool_result":
			payload := stringifyValue(block.Content)
			if payload == "" {
				payload = "{}"
			}
			name := strings.TrimSpace(block.Name)
			if name != "" {
				toolResults = append(toolResults, fmt.Sprintf("<|tool_result:%s|>\n%s\n", name, payload))
			} else if block.ToolUseID != "" {
				toolResults = append(toolResults, fmt.Sprintf("<|tool_result:%s|>\n%s\n", block.ToolUseID, payload))
			} else {
				toolResults = append(toolResults, "<|tool_result|>\n"+payload+"\n")
			}
		}
	}

	return sanitizeUTF8(strings.TrimSpace(strings.Join(textParts, "\n"))), toolResults
}

func renderConversationBlock(block renderedBlock) string {
	switch block.role {
	case "assistant":
		return "<|assistant|>\n" + block.text + "\n"
	case "tool":
		return block.text
	default:
		return "<|user|>\n" + block.text + "\n"
	}
}

func extractMessageText(content prompt.MessageContent) string {
	if content.IsString() {
		return sanitizeUTF8(strings.TrimSpace(content.GetText()))
	}

	var parts []string
	for _, block := range content.GetBlocks() {
		if block.Type == "text" {
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, sanitizeUTF8(text))
			}
		}
	}
	return sanitizeUTF8(strings.TrimSpace(strings.Join(parts, "\n")))
}

func sanitizeUTF8(text string) string {
	return strings.ToValidUTF8(text, "")
}

func stringifyValue(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return sanitizeUTF8(strings.TrimSpace(t))
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return sanitizeUTF8(fmt.Sprint(t))
		}
		return sanitizeUTF8(string(b))
	}
}

func appendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	buf = append(buf, byte(v))
	return buf
}

func EstimateInputTokens(promptText, _ string, messages []prompt.Message, _ []interface{}, _ bool) (InputTokenEstimate, error) {
	built := buildPrompt(promptText, messages, nil, nil, false, "")
	baseTokens := tiktoken.EstimateTextTokens(built.BasePrompt)
	historyTokens := tiktoken.EstimateTextTokens(built.HistoryText)
	toolResultTokens := tiktoken.EstimateTextTokens(built.ToolResultsText)
	queryTokens := tiktoken.EstimateTextTokens(built.Query)
	total := baseTokens + historyTokens + toolResultTokens

	return InputTokenEstimate{
		Profile:          "warp-codefreemax",
		QueryTokens:      queryTokens,
		BasePromptTokens: baseTokens,
		HistoryTokens:    historyTokens,
		ToolResultTokens: toolResultTokens,
		ToolSchemaTokens: 0,
		ToolCount:        0,
		Total:            total,
	}, nil
}

func ResolveModelAlias(model string) string {
	canonical := canonicalModelID(model)
	if canonical == "" {
		return ""
	}
	if _, ok := canonicalModelAliases[canonical]; ok {
		return canonical
	}
	return ""
}

func normalizeWarpTemplateModel(model string) string {
	canonical := canonicalModelID(model)
	if canonical == "" {
		return defaultModel
	}
	return canonical
}

func buildRequestBytesFromOfficialProto(userText, model string, disableWarpTools bool, workdir, conversationID string, tools []interface{}) ([]byte, error) {
	req, err := buildOfficialWarpRequest(userText, model, disableWarpTools, workdir, conversationID, tools)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(req)
}

func buildOfficialWarpRequest(userText, model string, disableWarpTools bool, workdir, conversationID string, tools []interface{}) (*v1.Request, error) {
	targetModel := strings.TrimSpace(model)
	if targetModel == "" {
		targetModel = defaultModel
	}

	userQuery := v1.Request_Input_UserQuery_builder{
		Query:         stringPtr(userText),
		Mode:          (&v1.UserQueryMode_builder{}).Build(),
		IntendedAgent: agentTypePtr(v1.AgentType_AGENT_TYPE_PRIMARY),
	}.Build()

	input := v1.Request_Input_builder{
		Context: buildOfficialInputContext(workdir),
		UserInputs: v1.Request_Input_UserInputs_builder{
			Inputs: []*v1.Request_Input_UserInputs_UserInput{
				v1.Request_Input_UserInputs_UserInput_builder{
					UserQuery: userQuery,
				}.Build(),
			},
		}.Build(),
	}.Build()

	settings := buildOfficialSettings(targetModel, disableWarpTools)
	metadata := buildOfficialMetadata(conversationID)

	var mcpContext *v1.Request_MCPContext
	if !disableWarpTools {
		var err error
		mcpContext, err = buildMCPContext(tools)
		if err != nil {
			return nil, err
		}
	}

	return v1.Request_builder{
		TaskContext: (&v1.Request_TaskContext_builder{}).Build(),
		Input:       input,
		Settings:    settings,
		Metadata:    metadata,
		McpContext:  mcpContext,
	}.Build(), nil
}

func buildOfficialInputContext(workdir string) *v1.InputContext {
	pwd := strings.TrimSpace(workdir)
	return v1.InputContext_builder{
		Directory: v1.InputContext_Directory_builder{
			Pwd:  stringPtr(pwd),
			Home: stringPtr(""),
		}.Build(),
		OperatingSystem: v1.InputContext_OperatingSystem_builder{
			Platform:     stringPtr("MacOS"),
			Distribution: stringPtr(""),
		}.Build(),
		Shell: v1.InputContext_Shell_builder{
			Name:    stringPtr("zsh"),
			Version: stringPtr("5.9"),
		}.Build(),
		CurrentTime: timestamppb.New(time.Now()),
	}.Build()
}

func buildOfficialSettings(model string, disableWarpTools bool) *v1.Request_Settings {
	settings := v1.Request_Settings_builder{
		ModelConfig: v1.Request_Settings_ModelConfig_builder{
			Base:     stringPtr(model),
			CliAgent: stringPtr(identifier),
		}.Build(),
		RulesEnabled:                       boolPtr(true),
		WebContextRetrievalEnabled:         boolPtr(true),
		SupportsParallelToolCalls:          boolPtr(true),
		PlanningEnabled:                    boolPtr(true),
		WarpDriveContextEnabled:            boolPtr(true),
		SupportsCreateFiles:                boolPtr(true),
		SupportsLongRunningCommands:        boolPtr(true),
		ShouldPreserveFileContentInHistory: boolPtr(true),
		SupportsTodosUi:                    boolPtr(true),
		SupportsLinkedCodeBlocks:           boolPtr(true),
		SupportsStartedChildTaskMessage:    boolPtr(true),
		SupportsSuggestPrompt:              boolPtr(true),
		SupportsReadImageFiles:             boolPtr(true),
		SupportsReasoningMessage:           boolPtr(true),
		WebSearchEnabled:                   boolPtr(true),
		SupportsV4AFileDiffs:               boolPtr(true),
	}
	if !disableWarpTools {
		settings.SupportedTools = officialSupportedTools()
		settings.SupportedCliAgentTools = officialSupportedCliAgentTools()
	}
	return settings.Build()
}

func buildOfficialMetadata(conversationID string) *v1.Request_Metadata {
	metadata := v1.Request_Metadata_builder{
		Logging: map[string]*structpb.Value{
			"entrypoint":                 structpb.NewStringValue("USER_INITIATED"),
			"is_auto_resume_after_error": structpb.NewBoolValue(false),
			"is_autodetected_user_query": structpb.NewBoolValue(true),
		},
	}
	if trimmed := strings.TrimSpace(conversationID); trimmed != "" {
		metadata.ConversationId = stringPtr(trimmed)
	}
	return metadata.Build()
}

func officialSupportedTools() []v1.ToolType {
	return []v1.ToolType{
		v1.ToolType_GREP,
		v1.ToolType_FILE_GLOB,
		v1.ToolType_FILE_GLOB_V2,
		v1.ToolType_READ_MCP_RESOURCE,
		v1.ToolType_CALL_MCP_TOOL,
		v1.ToolType_INIT_PROJECT,
		v1.ToolType_OPEN_CODE_REVIEW,
		v1.ToolType_RUN_SHELL_COMMAND,
		v1.ToolType_SUGGEST_NEW_CONVERSATION,
		v1.ToolType_SUBAGENT,
		v1.ToolType_WRITE_TO_LONG_RUNNING_SHELL_COMMAND,
		v1.ToolType_READ_SHELL_COMMAND_OUTPUT,
		v1.ToolType_READ_DOCUMENTS,
		v1.ToolType_EDIT_DOCUMENTS,
		v1.ToolType_CREATE_DOCUMENTS,
		v1.ToolType_READ_FILES,
		v1.ToolType_APPLY_FILE_DIFFS,
		v1.ToolType_SEARCH_CODEBASE,
		v1.ToolType_SUGGEST_PROMPT,
	}
}

func officialSupportedCliAgentTools() []v1.ToolType {
	return []v1.ToolType{
		v1.ToolType_GREP,
		v1.ToolType_FILE_GLOB,
		v1.ToolType_FILE_GLOB_V2,
		v1.ToolType_READ_FILES,
		v1.ToolType_SEARCH_CODEBASE,
	}
}

func buildMCPContext(tools []interface{}) (*v1.Request_MCPContext, error) {
	converted := convertTools(tools)
	if len(converted) == 0 {
		return nil, nil
	}

	mcpTools := make([]*v1.Request_MCPContext_MCPTool, 0, len(converted))
	for _, tool := range converted {
		var st *structpb.Struct
		if len(tool.Schema) > 0 {
			var err error
			st, err = structpb.NewStruct(tool.Schema)
			if err != nil {
				return nil, err
			}
		}
		mcpTools = append(mcpTools, v1.Request_MCPContext_MCPTool_builder{
			Name:        stringPtr(tool.Name),
			Description: stringPtr(tool.Description),
			InputSchema: st,
		}.Build())
	}

	server := v1.Request_MCPContext_MCPServer_builder{
		Name:        stringPtr("client"),
		Description: stringPtr("Client provided tools"),
		Id:          stringPtr("orchids-client-tools"),
		Tools:       mcpTools,
	}.Build()
	return v1.Request_MCPContext_builder{
		Servers: []*v1.Request_MCPContext_MCPServer{server},
	}.Build(), nil
}

func stringPtr(s string) *string { return &s }

func boolPtr(v bool) *bool { return &v }

func agentTypePtr(v v1.AgentType) *v1.AgentType { return &v }

type toolDef struct {
	Name        string
	Description string
	Schema      map[string]interface{}
}

const (
	maxWarpToolCount         = 32
	maxWarpToolDescLen       = 512
	maxWarpToolSchemaJSONLen = 4096
)

var warpBuiltinToolNames = map[string]struct{}{
	"Bash":      {},
	"Read":      {},
	"Edit":      {},
	"Write":     {},
	"Glob":      {},
	"Grep":      {},
	"TodoWrite": {},
}

var warpToolAllowedProps = map[string]map[string]struct{}{
	"Bash": {
		"command":           {},
		"description":       {},
		"run_in_background": {},
		"timeout":           {},
	},
	"Read": {
		"file_path": {},
		"offset":    {},
		"limit":     {},
		"pages":     {},
	},
	"Edit": {
		"file_path":   {},
		"old_string":  {},
		"new_string":  {},
		"replace_all": {},
	},
	"Write": {
		"file_path": {},
		"content":   {},
	},
	"Glob": {
		"pattern": {},
		"path":    {},
	},
	"Grep": {
		"pattern":     {},
		"path":        {},
		"glob":        {},
		"type":        {},
		"output_mode": {},
		"-i":          {},
		"multiline":   {},
		"head_limit":  {},
		"offset":      {},
		"context":     {},
	},
	"TodoWrite": {
		"todos": {},
	},
}

func isWarpBuiltinTool(name string) bool {
	_, ok := warpBuiltinToolNames[name]
	return ok
}

func convertTools(tools []interface{}) []toolDef {
	if len(tools) == 0 {
		return nil
	}

	defs := make([]toolDef, 0, len(tools))
	seen := make(map[string]struct{})
	for _, raw := range tools {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, description, schema := extractWarpToolSpecFields(m)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		canonicalName := orchids.NormalizeToolNameFallback(name)
		key := strings.ToLower(name)
		if isWarpBuiltinTool(canonicalName) {
			key = "builtin:" + strings.ToLower(canonicalName)
		}
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		schema = compactWarpSchemaForTool(canonicalName, schema)
		defs = append(defs, toolDef{
			Name:        name,
			Description: compactWarpDescription(description),
			Schema:      schema,
		})
		if len(defs) >= maxWarpToolCount {
			break
		}
	}
	return defs
}

func extractWarpToolSpecFields(tool map[string]interface{}) (string, string, map[string]interface{}) {
	if tool == nil {
		return "", "", nil
	}

	var name string
	var description string
	var schema map[string]interface{}

	if fn, ok := tool["function"].(map[string]interface{}); ok {
		if v, ok := fn["name"].(string); ok {
			name = v
		}
		if v, ok := fn["description"].(string); ok {
			description = v
		}
		schema = schemaMap(fn["parameters"])
		if schema == nil {
			schema = schemaMap(fn["input_schema"])
		}
	}
	if name == "" {
		if v, ok := tool["name"].(string); ok {
			name = v
		}
	}
	if description == "" {
		if v, ok := tool["description"].(string); ok {
			description = v
		}
	}
	if schema == nil {
		schema = schemaMap(tool["input_schema"])
	}
	if schema == nil {
		schema = schemaMap(tool["parameters"])
	}
	return name, description, schema
}

func schemaMap(v interface{}) map[string]interface{} {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func compactWarpDescription(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}
	const suffix = "...[truncated]"
	runes := []rune(description)
	if len(runes) <= maxWarpToolDescLen {
		return description
	}
	keep := maxWarpToolDescLen - len([]rune(suffix))
	if keep <= 0 {
		return suffix
	}
	return string(runes[:keep]) + suffix
}

func compactWarpSchemaForTool(name string, schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	cleaned := cleanWarpSchema(schema)
	if cleaned == nil {
		return nil
	}
	filtered := filterWarpSchemaProperties(name, cleaned)
	if filtered == nil {
		return nil
	}
	cleaned = filtered
	if warpSchemaJSONLen(cleaned) <= maxWarpToolSchemaJSONLen {
		return cleaned
	}
	stripped := stripWarpSchemaDescriptions(cleaned)
	if warpSchemaJSONLen(stripped) <= maxWarpToolSchemaJSONLen {
		return stripped
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func filterWarpSchemaProperties(name string, schema map[string]interface{}) map[string]interface{} {
	allowed, ok := warpToolAllowedProps[name]
	if !ok || schema == nil {
		return schema
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok || len(props) == 0 {
		return schema
	}

	filtered := make(map[string]interface{}, len(props))
	for key, value := range props {
		if _, keep := allowed[key]; keep {
			filtered[key] = value
		}
	}

	out := make(map[string]interface{}, len(schema))
	for key, value := range schema {
		switch key {
		case "properties":
			out[key] = filtered
		case "required":
			raw, ok := value.([]interface{})
			if !ok {
				out[key] = value
				continue
			}
			req := make([]interface{}, 0, len(raw))
			for _, item := range raw {
				propName, _ := item.(string)
				if _, keep := allowed[propName]; keep {
					req = append(req, item)
				}
			}
			if len(req) > 0 {
				out[key] = req
			}
		default:
			out[key] = value
		}
	}
	return out
}

func cleanWarpSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	sanitized := map[string]interface{}{}
	for _, key := range []string{"type", "description", "properties", "required", "enum", "items"} {
		if v, ok := schema[key]; ok {
			sanitized[key] = v
		}
	}
	if props, ok := sanitized["properties"].(map[string]interface{}); ok {
		cleanProps := map[string]interface{}{}
		for name, prop := range props {
			cleanProps[name] = cleanWarpSchemaValue(prop)
		}
		sanitized["properties"] = cleanProps
	}
	if items, ok := sanitized["items"]; ok {
		sanitized["items"] = cleanWarpSchemaValue(items)
	}
	return sanitized
}

func cleanWarpSchemaValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		return cleanWarpSchema(v)
	case []interface{}:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			out = append(out, cleanWarpSchemaValue(item))
		}
		return out
	default:
		return value
	}
}

func stripWarpSchemaDescriptions(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	out := make(map[string]interface{}, len(schema))
	for k, v := range schema {
		if strings.EqualFold(k, "description") || strings.EqualFold(k, "title") {
			continue
		}
		out[k] = stripWarpSchemaDescriptionsValue(v)
	}
	return out
}

func stripWarpSchemaDescriptionsValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		return stripWarpSchemaDescriptions(v)
	case []interface{}:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			out = append(out, stripWarpSchemaDescriptionsValue(item))
		}
		return out
	default:
		return value
	}
}

func warpSchemaJSONLen(schema map[string]interface{}) int {
	if schema == nil {
		return 0
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return 0
	}
	return len(raw)
}
