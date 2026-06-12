package store

import (
	"bytes"
	"github.com/goccy/go-json"
	"strings"
)

// ModelStatus represents the model status.
//
// The management frontend uses the string status: available/maintenance/offline.
// Old data/old clients may still use bool(true/false). Compatibility analysis is done here.
type ModelStatus string

const (
	ModelStatusAvailable   ModelStatus = "available"
	ModelStatusMaintenance ModelStatus = "maintenance"
	ModelStatusOffline     ModelStatus = "offline"
)

// Enabled indicates whether the model is available to the external /v1/models list.
func (s ModelStatus) Enabled() bool {
	return s == ModelStatusAvailable
}

func (s *ModelStatus) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*s = ModelStatusOffline
		return nil
	}

	// Compatible bool: true => available, false => offline
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		if b {
			*s = ModelStatusAvailable
		} else {
			*s = ModelStatusOffline
		}
		return nil
	}

	// string status
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		switch strings.ToLower(strings.TrimSpace(str)) {
		case "available", "enabled", "true", "on", "1":
			*s = ModelStatusAvailable
		case "maintenance", "maint":
			*s = ModelStatusMaintenance
		case "offline", "disabled", "false", "off", "0":
			*s = ModelStatusOffline
		default:
			*s = ModelStatusOffline
		}
		return nil
	}

	// Bottom line: illegal values ​​are considered offline
	*s = ModelStatusOffline
	return nil
}

func (s ModelStatus) MarshalJSON() ([]byte, error) {
	// Always output a string, ensuring that the previous backend is consistent.
	if s == "" {
		s = ModelStatusOffline
	}
	return json.Marshal(string(s))
}

type Model struct {
	ID        string      `json:"id"`
	Channel   string      `json:"channel"`  // e.g., "warp", "grok"
	ModelID   string      `json:"model_id"` // e.g., "claude-3-5-sonnet"
	Name      string      `json:"name"`     // e.g., "Claude 3.5 Sonnet"
	Status    ModelStatus `json:"status"`   // Enabled/Disabled
	Verified  bool        `json:"verified,omitempty"`
	IsDefault bool        `json:"is_default"` // Is default for this channel
	SortOrder int         `json:"sort_order"`
}
