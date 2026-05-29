package warp

import (
	"strings"
	"testing"

	v1 "github.com/warpdotdev/warp-proto-apis/apis/multi_agent/v1/gen/go"
	"google.golang.org/protobuf/proto"

	"orchids-api/internal/prompt"
	"orchids-api/internal/upstream"
)

func TestBuildRequestBytes_UsesCodeFreeMaxStylePrompt(t *testing.T) {
	req := upstream.UpstreamRequest{
		Prompt:  "ignored because messages are present",
		Model:   "claude-4-5-sonnet",
		Workdir: "/repo",
		Messages: []prompt.Message{
			{
				Role: "user",
				Content: prompt.MessageContent{
					Text: "check the project layout",
				},
			},
			{
				Role: "assistant",
				Content: prompt.MessageContent{
					Blocks: []prompt.ContentBlock{
						{Type: "text", Text: "I will inspect the repository."},
						{Type: "tool_use", ID: "call_1", Name: "Glob", Input: map[string]interface{}{"pattern": "**/*"}},
					},
				},
			},
			{
				Role: "user",
				Content: prompt.MessageContent{
					Blocks: []prompt.ContentBlock{
						{Type: "tool_result", ToolUseID: "call_1", Content: "./README.md\n./main.go"},
					},
				},
			},
		},
	}

	promptText, payload, err := buildRequestBytes(req)
	if err != nil {
		t.Fatalf("buildRequestBytes error: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("expected protobuf payload")
	}
	if !strings.Contains(promptText, "<|system_prompt|>") {
		t.Fatalf("prompt missing system prompt header: %q", promptText)
	}
	if strings.Contains(promptText, "Current working directory: /repo") {
		t.Fatalf("prompt should not include workdir: %q", promptText)
	}
	if strings.Contains(promptText, "<tool_call name=\"Glob\" id=\"call_1\">") {
		t.Fatalf("prompt should not include assistant tool call transcript: %q", promptText)
	}
	if !strings.Contains(promptText, "<|tool_result:call_1|>") {
		t.Fatalf("prompt missing tool result transcript: %q", promptText)
	}
	if !strings.Contains(promptText, "When executing commands, show the command and explain the output.") {
		t.Fatalf("prompt missing CodeFreeMax output guidance: %q", promptText)
	}

	var decoded v1.Request
	if err := proto.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload should decode as official Warp Request: %v", err)
	}
	inputs := decoded.GetInput().GetUserInputs().GetInputs()
	if len(inputs) != 1 {
		t.Fatalf("inputs len=%d want=1", len(inputs))
	}
	if got := inputs[0].GetUserQuery().GetQuery(); got != promptText {
		t.Fatalf("decoded query mismatch\n got: %q\nwant: %q", got, promptText)
	}
	modelConfig := decoded.GetSettings().GetModelConfig()
	if got := modelConfig.GetBase(); got != "claude-4-5-sonnet" {
		t.Fatalf("model_config.base=%q want claude-4-5-sonnet", got)
	}
	if got := modelConfig.GetCliAgent(); got != identifier {
		t.Fatalf("model_config.cli_agent=%q want %q", got, identifier)
	}
	logging := decoded.GetMetadata().GetLogging()
	if got := logging["entrypoint"].GetStringValue(); got != "USER_INITIATED" {
		t.Fatalf("logging.entrypoint=%q want USER_INITIATED", got)
	}
	if got := logging["is_auto_resume_after_error"].GetBoolValue(); got {
		t.Fatalf("logging.is_auto_resume_after_error=%v want false", got)
	}
	if got := logging["is_autodetected_user_query"].GetBoolValue(); !got {
		t.Fatalf("logging.is_autodetected_user_query=%v want true", got)
	}
}

func TestBuildRequestBytes_UsesOfficialConversationID(t *testing.T) {
	req := upstream.UpstreamRequest{
		Prompt:        "continue",
		Model:         "claude-4-5-opus",
		ChatSessionID: "conv_123",
	}

	_, payload, err := buildRequestBytes(req)
	if err != nil {
		t.Fatalf("buildRequestBytes error: %v", err)
	}

	var decoded v1.Request
	if err := proto.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload should decode as official Warp Request: %v", err)
	}
	if got := decoded.GetMetadata().GetConversationId(); got != "conv_123" {
		t.Fatalf("conversation_id=%q want conv_123", got)
	}
}

func TestBuildRequestBytes_UsesGroupedMCPServers(t *testing.T) {
	req := upstream.UpstreamRequest{
		Prompt: "search symbols",
		Model:  "claude-4-5-opus",
		Tools: []interface{}{
			map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "workspace_search",
					"description": "search project symbols",
					"parameters": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"query": map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
	}

	_, payload, err := buildRequestBytes(req)
	if err != nil {
		t.Fatalf("buildRequestBytes error: %v", err)
	}

	var decoded v1.Request
	if err := proto.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload should decode as official Warp Request: %v", err)
	}

	if got := len(decoded.GetMcpContext().GetTools()); got != 0 {
		t.Fatalf("deprecated mcp_context.tools len=%d want 0", got)
	}
	servers := decoded.GetMcpContext().GetServers()
	if len(servers) != 1 {
		t.Fatalf("mcp_context.servers len=%d want 1", len(servers))
	}
	if got := servers[0].GetId(); got != "orchids-client-tools" {
		t.Fatalf("mcp server id=%q want orchids-client-tools", got)
	}
	tools := servers[0].GetTools()
	if len(tools) != 1 {
		t.Fatalf("mcp server tools len=%d want 1", len(tools))
	}
	if got := tools[0].GetName(); got != "workspace_search" {
		t.Fatalf("mcp tool name=%q want workspace_search", got)
	}
	if tools[0].GetInputSchema().GetFields()["properties"] == nil {
		t.Fatalf("mcp tool schema lost properties: %#v", tools[0].GetInputSchema())
	}
}

func TestEstimateInputTokens_CodeFreeMaxProfile(t *testing.T) {
	estimate, err := EstimateInputTokens("say hi", "gpt-4o", nil, nil, false)
	if err != nil {
		t.Fatalf("EstimateInputTokens error: %v", err)
	}
	if estimate.Profile != "warp-codefreemax" {
		t.Fatalf("profile=%q want warp-codefreemax", estimate.Profile)
	}
	if estimate.Total <= 0 {
		t.Fatalf("expected positive total tokens, got %d", estimate.Total)
	}
}

func TestConvertTools_PreservesCustomMCPTools(t *testing.T) {
	t.Parallel()

	tools := []interface{}{
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "workspace_search",
				"description": strings.Repeat("search project symbols ", 40),
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "term to search for",
						},
						"top_k": map[string]interface{}{
							"type": "integer",
						},
					},
				},
			},
		},
		map[string]interface{}{
			"name":        "Read",
			"description": "read file",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string"},
				},
			},
		},
	}

	got := convertTools(tools)
	if len(got) != 2 {
		t.Fatalf("convertTools len=%d want=2 (%#v)", len(got), got)
	}
	if got[0].Name != "workspace_search" {
		t.Fatalf("custom tool name=%q want workspace_search", got[0].Name)
	}
	if !strings.HasSuffix(got[0].Description, "...[truncated]") {
		t.Fatalf("custom tool description=%q want truncated suffix", got[0].Description)
	}
	props, ok := got[0].Schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("custom tool properties type=%T", got[0].Schema["properties"])
	}
	if _, ok := props["query"]; !ok {
		t.Fatalf("custom tool schema lost query property: %#v", got[0].Schema)
	}
	if got[1].Name != "Read" {
		t.Fatalf("builtin tool name=%q want Read", got[1].Name)
	}
}
