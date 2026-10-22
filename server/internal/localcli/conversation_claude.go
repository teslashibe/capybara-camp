package localcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

type claudeSteer struct {
	id, text string
	deadline time.Time
	ack      bool
	boundary bool
	sent     bool
}

func claudeTurn(connection turnConnection, request conversationRequest, controls <-chan map[string]any, emit func(map[string]any) error) error {
	session := request.Session
	user := func(text, id string) error {
		return connection.Write(map[string]any{"type": "user", "uuid": id, "session_id": session.SessionID, "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": text}})
	}
	implementation := request.Mode == "implementation"
	var relay *approvalRelay
	initialization := ""
	var initializeDeadline time.Time
	if implementation {
		relay = newApprovalRelay(connection, emit, "Claude", session.SessionID, "")
		initialization = uuid.NewString()
		initializeDeadline = time.Now().Add(30 * time.Second)
		if err := connection.Write(map[string]any{"type": "control_request", "request_id": initialization, "request": map[string]any{"subtype": "initialize", "hooks": nil}}); err != nil {
			return err
		}
	} else if err := user(request.Prompt, uuid.NewString()); err != nil {
		return err
	}
	var pending *claudeSteer
	text, reportedSpeed := "", ""
	reported := runtimeSettings{}
	ready := false
	for {
		select {
		case control := <-controls:
			switch str(control["type"]) {
			case "stop":
				return context.Canceled
			case "approval":
				if relay != nil {
					if err := relay.Resolve(control); err != nil {
						return err
					}
				}
			case "steer":
				id, value := str(control["id"]), str(control["text"])
				message := ""
				if request.Mode == "planning" {
					message = "Restart the plan to change its frozen brief"
				} else if pending != nil || !ready {
					message = "Wait until the current steering request finishes"
				} else if !validSessionID(id) || strings.TrimSpace(value) == "" || len(value) > 65536 {
					message = "Write a steering message under 64 KB"
				}
				if message != "" {
					if err := emit(map[string]any{"type": "steerRejected", "requestID": id, "message": message}); err != nil {
						return err
					}
				} else {
					pending = &claudeSteer{id: id, text: value, deadline: time.Now().Add(15 * time.Second)}
					if err := connection.Write(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "interrupt"}}); err != nil {
						return err
					}
				}
			}
		default:
		}
		if initialization != "" && time.Now().After(initializeDeadline) {
			return errors.New("Claude did not initialize its approval channel")
		}
		if pending != nil && time.Now().After(pending.deadline) {
			if err := emit(map[string]any{"type": "steerRejected", "requestID": pending.id, "message": "Steering acknowledgment timed out; check the transcript before retrying"}); err != nil {
				return err
			}
			return errors.New("Claude's steering response was uncertain. The queue is paused")
		}
		event, err := connection.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("Claude closed before completing the turn")
		}
		if err != nil {
			return err
		}
		if event == nil {
			continue
		}
		kind := str(event["type"])
		if relay == nil && kind == "control_request" {
			if err := connection.Write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": event["request_id"], "response": map[string]any{"behavior": "deny", "message": "Project tools are disabled in Camp's chat preview"}}}); err != nil {
				return err
			}
			continue
		}
		if relay != nil && kind == "control_request" {
			if !ready {
				return errors.New("Claude requested a tool before confirming the session settings")
			}
			if err := relay.Request(event); err != nil {
				return err
			}
			continue
		}
		if relay != nil && kind == "control_cancel_request" {
			if err := relay.Cancelled(event["request_id"]); err != nil {
				return err
			}
			continue
		}
		response := object(event["response"])
		if initialization != "" && kind == "control_response" && str(response["request_id"]) == initialization {
			if str(response["subtype"]) != "success" {
				return errors.New("Claude rejected its approval channel")
			}
			initialization = ""
			if request.NewSession {
				if err := emit(map[string]any{"type": "session", "sessionID": session.SessionID}); err != nil {
					return err
				}
			}
			if err := user(request.Prompt, uuid.NewString()); err != nil {
				return err
			}
			continue
		}
		if other := str(event["session_id"]); other != "" && other != session.SessionID {
			continue
		}
		if kind == "system" && str(event["subtype"]) == "init" {
			reported.Model = observedModel(event["model"])
			reported.Effort = observedEffort(event["effort"])
			if reported.Effort == "" {
				reported.Effort = request.Effort
			}
			switch str(event["fast_mode_state"]) {
			case "on":
				reported.Speed = "fast"
			case "off":
				reported.Speed = "standard"
			}
			if implementation {
				actual, selected := str(event["permissionMode"]), request.Permissions["claudeMode"]
				if selected != "" && actual != selected && !(selected == "manual" && actual == "default") {
					return errors.New("Claude did not confirm the selected permission mode")
				}
				if actual == "" {
					actual = "Claude project rules"
				}
				if err := emit(map[string]any{"type": "permissions", "text": actual}); err != nil {
					return err
				}
			}
			if request.grant != nil {
				connected := false
				for _, value := range array(event["mcp_servers"]) {
					server := object(value)
					connected = connected || str(server["name"]) == "camp_context" && str(server["status"]) == "connected"
				}
				if !connected {
					return errors.New("The selected context tool did not connect. No context was supplied")
				}
			}
			if !ready {
				if err := emitSettings(emit, reported); err != nil {
					return err
				}
				ready = true
				if err := emit(map[string]any{"type": "started"}); err != nil {
					return err
				}
			}
			if request.Speed == "fast" && str(event["fast_mode_state"]) == "off" {
				if err := emit(map[string]any{"type": "notice", "message": "Claude did not enable Fast mode. Check usage credits and account availability."}); err != nil {
					return err
				}
			}
		}
		if kind == "control_response" && pending != nil && str(response["request_id"]) == pending.id {
			if str(response["subtype"]) != "success" {
				message := str(response["error"])
				if message == "" {
					message = "Claude rejected the interruption"
				}
				if err := emit(map[string]any{"type": "steerRejected", "requestID": pending.id, "message": message}); err != nil {
					return err
				}
				ended := pending.boundary
				pending = nil
				if ended {
					return emit(map[string]any{"type": "done"})
				}
			} else {
				pending.ack = true
			}
		}
		if kind == "user" && pending != nil && pending.sent && str(event["uuid"]) == pending.id {
			if err := emit(map[string]any{"type": "steerAccepted", "requestID": pending.id}); err != nil {
				return err
			}
			pending = nil
		}
		if event["parent_tool_use_id"] == nil || event["parent_tool_use_id"] == "" {
			switch kind {
			case "stream_event":
				delta := object(object(event["event"])["delta"])
				if str(delta["type"]) == "text_delta" {
					if len(text)+len(str(delta["text"])) > maxConversationText {
						return errors.New("The conversation response exceeded 2 MB")
					}
					text += str(delta["text"])
					if err := emitConversationText(request, emit, text); err != nil {
						return err
					}
				}
			case "assistant":
				message := object(event["message"])
				if model := observedModel(message["model"]); model != "" {
					reported.Model = model
				}
				if effort := observedEffort(event["effort"]); effort != "" {
					reported.Effort = effort
				}
				speed := str(object(message["usage"])["speed"])
				if value := observedSpeed(speed); value != "" {
					reported.Speed = value
				}
				if err := emitSettings(emit, reported); err != nil {
					return err
				}
				if (speed == "standard" || speed == "fast") && speed != reportedSpeed {
					reportedSpeed = speed
					if request.Speed != "" && speed != request.Speed {
						if err := emit(map[string]any{"type": "notice", "message": fmt.Sprintf("Claude returned this reply at %s speed (%s was requested). Check usage credits and account availability.", speed, request.Speed)}); err != nil {
							return err
						}
					}
				}
				if complete := textBlocks(message["content"]); complete != "" {
					text = complete
					if err := emitConversationText(request, emit, text); err != nil {
						return err
					}
				}
			case "result":
				if value, exists := event["is_error"]; exists {
					if _, ok := value.(bool); !ok {
						return errors.New("Claude returned an invalid completion state")
					}
				}
				if err := emitUsage(emit, object(event["usage"]), false); err != nil {
					return err
				}
				if pending != nil && !pending.sent {
					pending.boundary = true
				} else {
					if pending != nil {
						if err := emit(map[string]any{"type": "steerRejected", "requestID": pending.id, "message": "Claude did not acknowledge the steering message; check the transcript before retrying"}); err != nil {
							return err
						}
						return errors.New("Claude's steering response was uncertain. The queue is paused")
					}
					if event["is_error"] == true {
						message := str(event["result"])
						if message == "" {
							parts := []string{}
							for _, value := range array(event["errors"]) {
								if str(value) != "" {
									parts = append(parts, str(value))
								}
							}
							message = strings.Join(parts, "; ")
						}
						if message == "" {
							message = "Claude did not complete the turn"
						}
						return emit(map[string]any{"type": "error", "message": message})
					}
					if !ready {
						return errors.New("Claude finished without confirming the session")
					}
					if str(event["result"]) != "" {
						if err := emitConversationText(request, emit, str(event["result"])); err != nil {
							return err
						}
					}
					return emit(map[string]any{"type": "done"})
				}
			}
		}
		if kind == "system" && str(event["subtype"]) == "notification" && str(event["message"]) != "" {
			if err := emit(map[string]any{"type": "notice", "message": str(event["message"])}); err != nil {
				return err
			}
		}
		if pending != nil && !pending.sent && pending.ack && pending.boundary {
			if err := emit(map[string]any{"type": "boundary"}); err != nil {
				return err
			}
			text = ""
			if err := user(pending.text, pending.id); err != nil {
				return err
			}
			pending.sent = true
		}
	}
}
