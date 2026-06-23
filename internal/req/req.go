package req

import (
	"encoding/json"
	"fmt"

	"orchids-api/internal/prompt"
)

type Request struct {
	Model          string                 `json:"model"`
	Messages       []prompt.Message       `json:"messages"`
	System         SystemItems            `json:"system"`
	Tools          []interface{}          `json:"tools"`
	ToolChoice     interface{}            `json:"tool_choice"`
	Stream         bool                   `json:"stream"`
	ConversationID string                 `json:"conversation_id"`
	Metadata       map[string]interface{} `json:"metadata"`

	RawBody []byte `json:"-"`
}

type SystemItem struct {
	Type         string              `json:"type"`
	Text         string              `json:"text"`
	CacheControl *prompt.CacheControl `json:"cache_control,omitempty"`
}

type SystemItems []SystemItem

func (s *SystemItems) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*s = nil
		return nil
	}

	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*s = []SystemItem{{Type: "text", Text: text}}
		return nil
	}

	var items []SystemItem
	if err := json.Unmarshal(data, &items); err == nil {
		*s = items
		return nil
	}

	var item SystemItem
	if err := json.Unmarshal(data, &item); err == nil {
		*s = []SystemItem{item}
		return nil
	}

	return fmt.Errorf("system must be string or array")
}

type Parser func(body []byte) (*Request, error)

type Hook func(req *Request) error
