package localcli

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

func codexTurn(connection turnConnection, request conversationRequest, controls <-chan map[string]any, emit func(map[string]any) error) error {
	session := request.Session
	if _, err := connection.RPC("initialize", map[string]any{"clientInfo": map[string]any{"name": "capybara_camp", "title": "Capybara Camp", "version": "0.0.1"}, "capabilities": map[string]any{"experimentalApi": true}}, 0); err != nil {
		return err
	}
	if err := connection.Write(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	configResponse, err := connection.RPC("config/read", map[string]any{"includeLayers": false, "cwd": session.CWD}, 0)
	if err != nil {
		return err
	}
	config, ok := configResponse["config"].(map[string]any)
	if !ok {
		return errors.New("Codex did not return its active configuration")
	}
	implementation := request.Mode == "implementation"
	params := map[string]any{"threadId": session.SessionID, "cwd": session.CWD, "sandbox": "read-only", "approvalPolicy": "on-request", "config": codexChatConfig(config, request.grant),
		"developerInstructions": "This is a chat-only preview. Answer directly. Only the Camp context read tool, when configured, is permitted; do not run project tools or delegate work."}
	if request.grant != nil {
		params["developerInstructions"] = str(params["developerInstructions"]) + request.grant.instructions()
	}
	if implementation {
		params = map[string]any{"threadId": session.SessionID, "cwd": session.CWD}
		if len(request.Permissions) > 0 {
			response, err := connection.RPC("configRequirements/read", map[string]any{}, 0)
			if err != nil {
				return err
			}
			requirements := map[string]any{}
			if response["requirements"] != nil {
				var ok bool
				requirements, ok = response["requirements"].(map[string]any)
				if !ok {
					return errors.New("Codex returned invalid managed permission requirements")
				}
			}
			if _, err := checkCodexPermissions(request.Permissions, config, requirements); err != nil {
				return err
			}
			overrides, err := codexOverrides(request.Permissions)
			if err != nil {
				return err
			}
			for key, value := range overrides {
				params[key] = value
			}
		}
		if request.grant != nil {
			servers, _ := cloneObject(object(config["mcp_servers"]))
			if servers == nil {
				servers = map[string]any{}
			}
			camp, _ := cloneObject(request.grant.server)
			camp["enabled"], camp["required"] = true, true
			servers["camp_context"] = camp
			params["config"] = map[string]any{"mcp_servers": servers}
			params["developerInstructions"] = str(config["developer_instructions"]) + request.grant.instructions()
		}
	}
	if request.Model != "" {
		params["model"] = request.Model
	}
	method := "thread/resume"
	if request.NewSession || request.Mode == "planning" {
		method = "thread/start"
		delete(params, "threadId")
	}
	if request.Mode == "planning" {
		params["ephemeral"] = true
		params["developerInstructions"] = str(params["developerInstructions"]) + " This is one bounded planning step. Do not execute the plan or change project files."
	}
	response, err := connection.RPC(method, params, 0)
	if err != nil {
		return err
	}
	if method == "thread/start" {
		session.SessionID = str(object(response["thread"])["id"])
		if !validSessionID(session.SessionID) {
			return errors.New("Codex did not return a valid new session identity")
		}
		if request.NewSession {
			if err := emit(map[string]any{"type": "session", "sessionID": session.SessionID}); err != nil {
				return err
			}
		}
	}
	if implementation {
		overrides, _ := codexOverrides(request.Permissions)
		for key, value := range overrides {
			actual := str(response[key])
			if key == "permissions" {
				actual = str(object(response["activePermissionProfile"])["id"])
			} else if key == "sandbox" {
				actual = sandboxMode(response)
			}
			if actual != str(value) {
				return errors.New("Codex did not confirm the selected permission settings; no prompt was submitted")
			}
		}
		if err := emit(map[string]any{"type": "permissions", "text": codexPermissionSummary(response)}); err != nil {
			return err
		}
	}
	turnParams := map[string]any{"threadId": session.SessionID, "input": []any{map[string]any{"type": "text", "text": request.Prompt}}}
	reported := runtimeSettings{Model: observedModel(response["model"]), Effort: observedEffort(response["reasoningEffort"]), Speed: observedSpeed(response["serviceTier"])}
	if !implementation {
		turnParams["approvalPolicy"] = "on-request"
	}
	if request.Model != "" {
		turnParams["model"] = request.Model
	}
	if request.Effort != "" {
		turnParams["effort"] = request.Effort
	}
	if request.Speed != "" {
		tier := request.Speed
		if tier == "standard" {
			tier = "default"
		}
		turnParams["serviceTier"] = tier
	}
	response, err = connection.RPC("turn/start", turnParams, 0)
	if err != nil {
		return err
	}
	turnID := str(object(response["turn"])["id"])
	if !validSessionID(turnID) {
		return errors.New("Codex did not return a valid turn identity")
	}
	if request.Model != "" {
		reported.Model = request.Model
	}
	if request.Effort != "" {
		reported.Effort = request.Effort
	}
	if request.Speed != "" {
		reported.Speed = observedSpeed(request.Speed)
	}
	if err := emitSettings(emit, reported); err != nil {
		return err
	}
	var relay *approvalRelay
	if implementation {
		relay = newApprovalRelay(connection, emit, "Codex", session.SessionID, turnID)
	}
	if err := emit(map[string]any{"type": "started", "turnID": turnID}); err != nil {
		return err
	}
	texts, order, total := map[string]string{}, []string{}, 0
	for {
		select {
		case control := <-controls:
			if str(control["type"]) == "stop" {
				_, _ = connection.RPC("turn/interrupt", map[string]any{"threadId": session.SessionID, "turnId": turnID}, 3*time.Second)
				return context.Canceled
			} else if str(control["type"]) == "approval" && relay != nil {
				if err := relay.Resolve(control); err != nil {
					return err
				}
			} else if str(control["type"]) == "steer" {
				id, text := str(control["id"]), str(control["text"])
				var failure error
				attempted := false
				if request.Mode == "planning" {
					failure = errors.New("Restart the plan to change its frozen brief")
				} else if !validSessionID(id) || strings.TrimSpace(text) == "" || len(text) > 65536 {
					failure = errors.New("Write a steering message under 64 KB")
				} else {
					attempted = true
					var ack map[string]any
					ack, failure = connection.RPC("turn/steer", map[string]any{"threadId": session.SessionID, "expectedTurnId": turnID, "clientUserMessageId": id, "input": []any{map[string]any{"type": "text", "text": text}}}, 0)
					if failure == nil && ack["turnId"] != nil && str(ack["turnId"]) != turnID {
						failure = errors.New("Codex acknowledged a different turn; check the transcript before retrying")
					}
				}
				event := map[string]any{"type": "steerAccepted", "requestID": id}
				if failure != nil {
					event["type"], event["message"] = "steerRejected", failure.Error()
				}
				if err := emit(event); err != nil {
					return err
				}
				if failure != nil && attempted {
					var rejected *nativeRejection
					if !errors.As(failure, &rejected) {
						return errors.New("Codex's steering response was uncertain. Check the transcript before retrying; the queue is paused")
					}
				}
			}
		default:
		}
		event, err := connection.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("Codex closed before completing the turn")
		}
		if err != nil {
			return err
		}
		if event == nil {
			continue
		}
		if _, request := event["id"]; request && str(event["method"]) != "" {
			if relay != nil {
				err = relay.Request(event)
			} else {
				err = connection.RejectTool(event)
			}
			if err != nil {
				return err
			}
			continue
		}
		params := object(event["params"])
		if params["threadId"] != nil && str(params["threadId"]) != session.SessionID || params["turnId"] != nil && str(params["turnId"]) != turnID {
			continue
		}
		method := str(event["method"])
		if relay != nil && method == "serverRequest/resolved" {
			if err := relay.Cancelled(params["requestId"]); err != nil {
				return err
			}
		}
		if relay != nil && method == "item/started" {
			item := object(params["item"])
			relay.Remember(item)
			kind := str(item["type"])
			if kind == "commandExecution" || kind == "fileChange" || kind == "mcpToolCall" {
				text := str(item["command"])
				if text == "" {
					text = str(item["tool"])
				}
				if text == "" {
					text = "Updating project files"
				}
				if err := emit(map[string]any{"type": "activity", "text": text}); err != nil {
					return err
				}
			}
		}
		if method == "thread/tokenUsage/updated" {
			if err := emitUsage(emit, object(object(params["tokenUsage"])["total"]), true); err != nil {
				return err
			}
		}
		id, text, update := "", "", false
		if method == "item/agentMessage/delta" {
			id = str(params["itemId"])
			text, update = texts[id]+str(params["delta"]), true
		} else if method == "item/completed" && str(object(params["item"])["type"]) == "agentMessage" {
			id, text, update = str(object(params["item"])["id"]), str(object(params["item"])["text"]), true
		}
		if update {
			if !validSessionID(id) || len(text) > maxConversationText || total-len(texts[id])+len(text) > maxConversationText {
				return errors.New("Codex response exceeded its item or text limit")
			}
			if _, known := texts[id]; !known {
				if len(order) >= 1024 {
					return errors.New("Codex returned too many message items")
				}
				order = append(order, id)
			}
			total += len(text) - len(texts[id])
			texts[id] = text
			parts := make([]string, 0, len(order))
			for _, itemID := range order {
				parts = append(parts, texts[itemID])
			}
			if err := emitConversationText(request, emit, strings.Join(parts, "\n\n")); err != nil {
				return err
			}
		}
		if method == "turn/completed" && str(object(params["turn"])["id"]) == turnID {
			turn := object(params["turn"])
			if str(turn["status"]) != "completed" {
				message := str(object(turn["error"])["message"])
				if message == "" {
					message = "Codex did not complete the turn"
				}
				return emit(map[string]any{"type": "error", "message": message})
			}
			return emit(map[string]any{"type": "done"})
		}
	}
}
