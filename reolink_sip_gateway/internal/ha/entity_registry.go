package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type registryEnvelope struct {
	ID      int             `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

type registryEntity struct {
	EntityID       string  `json:"ei"`
	Platform       string  `json:"pl"`
	TranslationKey *string `json:"tk"`
}

type registryDisplayResult struct {
	Entities []registryEntity `json:"entities"`
}

// ResolveReolinkVisitorEntity returns the single enabled visitor binary sensor
// registered by Home Assistant's Reolink integration. It intentionally relies
// on entity-registry metadata rather than guessing an entity_id from a device
// name, so user-renamed entities continue to resolve correctly.
func ResolveReolinkVisitorEntity(ctx context.Context, wsURL, token string) (string, error) {
	if wsURL == "" {
		wsURL = "ws://supervisor/core/websocket"
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("Home Assistant access token is empty")
	}

	conn, err := dialWebSocket(ctx, wsURL)
	if err != nil {
		return "", fmt.Errorf("connect Home Assistant websocket: %w", err)
	}
	defer conn.Close()

	raw, err := conn.ReadMessage(ctx)
	if err != nil {
		return "", fmt.Errorf("read Home Assistant websocket auth challenge: %w", err)
	}
	var challenge registryEnvelope
	if err := json.Unmarshal(raw, &challenge); err != nil || challenge.Type != "auth_required" {
		return "", fmt.Errorf("unexpected Home Assistant websocket auth challenge: %s", strings.TrimSpace(string(raw)))
	}

	auth, _ := json.Marshal(map[string]any{"type": "auth", "access_token": token})
	if err := conn.WriteJSON(auth); err != nil {
		return "", fmt.Errorf("send Home Assistant websocket auth: %w", err)
	}
	raw, err = conn.ReadMessage(ctx)
	if err != nil {
		return "", fmt.Errorf("read Home Assistant websocket auth result: %w", err)
	}
	var authResult registryEnvelope
	if err := json.Unmarshal(raw, &authResult); err != nil {
		return "", fmt.Errorf("decode Home Assistant websocket auth result: %w", err)
	}
	if authResult.Type != "auth_ok" {
		return "", fmt.Errorf("Home Assistant websocket authentication failed: %s", authResult.Message)
	}

	request, _ := json.Marshal(map[string]any{"id": 1, "type": "config/entity_registry/list_for_display"})
	if err := conn.WriteJSON(request); err != nil {
		return "", fmt.Errorf("request Home Assistant entity registry: %w", err)
	}
	raw, err = conn.ReadMessage(ctx)
	if err != nil {
		return "", fmt.Errorf("read Home Assistant entity registry: %w", err)
	}
	var response registryEnvelope
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("decode Home Assistant entity registry response: %w", err)
	}
	if response.Type != "result" || response.ID != 1 || !response.Success {
		return "", fmt.Errorf("Home Assistant rejected entity-registry request: %s", response.Message)
	}

	var display registryDisplayResult
	if err := json.Unmarshal(response.Result, &display); err != nil {
		return "", fmt.Errorf("decode Home Assistant entity registry entries: %w", err)
	}

	var enabled []string
	for _, entity := range display.Entities {
		if !strings.HasPrefix(entity.EntityID, "binary_sensor.") || !strings.EqualFold(entity.Platform, "reolink") {
			continue
		}
		if entity.TranslationKey == nil || !strings.EqualFold(strings.TrimSpace(*entity.TranslationKey), "visitor") {
			continue
		}
		enabled = append(enabled, entity.EntityID)
	}
	sort.Strings(enabled)

	switch len(enabled) {
	case 1:
		return enabled[0], nil
	case 0:
		return "", fmt.Errorf("no enabled Reolink visitor binary sensor found in the Home Assistant entity registry; enable it in Home Assistant or set visitor_entity manually")
	default:
		return "", fmt.Errorf("multiple enabled Reolink visitor binary sensors found: %s; set visitor_entity manually", strings.Join(enabled, ", "))
	}
}
