package localcli

import (
	"context"
	"strings"
)

// These are provider-reported values, never profile defaults or Camp's next-turn
// selections. Empty values mean the provider did not report the setting.
type runtimeSettings struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Speed  string `json:"speed,omitempty"`
}

func observedEffort(value any) string {
	switch s := str(value); s {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return s
	}
	return ""
}
func observedSpeed(value any) string {
	switch s := str(value); s {
	case "default", "standard":
		return "standard"
	case "priority", "fast":
		return "fast"
	case "ultrafast":
		return "ultrafast"
	}
	return ""
}
func observedModel(value any) string {
	s := str(value)
	if len(s) > 160 || s == "<synthetic>" || strings.ContainsAny(s, "\r\n\x00") {
		return ""
	}
	return s
}
func (s runtimeSettings) apply(session *Session) {
	if s.Model != "" {
		session.Model = s.Model
	}
	session.Effort = s.Effort
	session.Speed = s.Speed
}
func emitSettings(emit func(map[string]any) error, s runtimeSettings) error {
	return emit(map[string]any{"type": "settings", "model": s.Model, "effort": s.Effort, "speed": s.Speed})
}

type settingsReader struct{ current, next runtimeSettings }

func claudeMessageSettings(d map[string]any) runtimeSettings {
	message := object(d["message"])
	speed := observedSpeed(object(message["usage"])["speed"])
	if speed == "" {
		speed = observedSpeed(d["speed"])
	}
	return runtimeSettings{Model: observedModel(message["model"]), Effort: observedEffort(d["effort"]), Speed: speed}
}

func (r *settingsReader) consume(provider string, d map[string]any) {
	if provider == "Claude" {
		if str(d["type"]) != "assistant" || boolean(d["isSidechain"]) || (d["parent_tool_use_id"] != nil && d["parent_tool_use_id"] != "") {
			return
		}
		settings := claudeMessageSettings(d)
		if settings.Model == "" {
			return
		}
		r.current = settings
		return
	}
	p := object(d["payload"])
	switch str(d["type"]) {
	case "event_msg":
		settings := object(p["thread_settings"])
		if len(settings) == 0 {
			return
		}
		// Settings updates may target the NEXT turn while this one is still running.
		r.next = runtimeSettings{Model: observedModel(settings["model"]), Effort: observedEffort(settings["reasoning_effort"]), Speed: observedSpeed(settings["service_tier"])}
	case "turn_context":
		r.current = runtimeSettings{Model: observedModel(p["model"]), Effort: observedEffort(p["effort"]), Speed: r.next.Speed}
		if value, ok := p["service_tier"]; ok {
			r.current.Speed = observedSpeed(value)
		}
	}
}
func lastSettings(ctx context.Context, provider, path string) runtimeSettings {
	var reader settingsReader
	_ = tailLines(ctx, path, 256<<10, func(_ int, d map[string]any) error { reader.consume(provider, d); return nil })
	return reader.current
}
