package localcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// An approval belongs to one running provider turn. Nothing from a later UI
// response may expand the provider's original request or offered decisions.
type approvalConnection interface {
	Write(map[string]any) error
	RejectTool(map[string]any) error
}

type approvalQuestion struct {
	ID        string   `json:"id"`
	Header    string   `json:"header"`
	Prompt    string   `json:"prompt"`
	Options   []string `json:"options"`
	Multiple  bool     `json:"multiple"`
	Secret    bool     `json:"secret"`
	AllowText bool     `json:"allowText"`
}

type approvalDecision struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Allows bool   `json:"allows"`
	Value  any    `json:"-"`
}

type pendingApproval struct {
	requestID  any
	kind       string
	params     map[string]any
	questions  []approvalQuestion
	decisions  []approvalDecision
	canApprove bool
}

type approvalRelay struct {
	connection approvalConnection
	emit       func(map[string]any) error
	provider   string
	sessionID  string
	turnID     string
	pending    map[string]*pendingApproval
	items      map[string]map[string]any
	itemOrder  []string
}

func newApprovalRelay(connection approvalConnection, emit func(map[string]any) error, provider, sessionID, turnID string) *approvalRelay {
	return &approvalRelay{connection: connection, emit: emit, provider: provider, sessionID: sessionID, turnID: turnID,
		pending: make(map[string]*pendingApproval), items: make(map[string]map[string]any)}
}

func cloneObject(value map[string]any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	err = decoder.Decode(&result)
	return result, err
}

func (r *approvalRelay) Remember(item map[string]any) {
	id := str(item["id"])
	if str(item["type"]) != "fileChange" || id == "" {
		return
	}
	if _, exists := r.items[id]; !exists {
		if len(r.itemOrder) == 16 {
			delete(r.items, r.itemOrder[0])
			r.itemOrder = r.itemOrder[1:]
		}
		r.itemOrder = append(r.itemOrder, id)
	}
	// An oversized replacement invalidates an older preview of the same item.
	r.items[id] = nil
	if data, err := json.Marshal(item); err == nil && len(data) <= 64<<10 {
		r.items[id], _ = cloneObject(item)
	}
}

func (r *approvalRelay) Cancelled(requestID any) error {
	for key, pending := range r.pending {
		if sameID(pending.requestID, requestID) {
			delete(r.pending, key)
			if err := r.emit(map[string]any{"type": "approvalResolved", "requestID": key}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *approvalRelay) Request(event map[string]any) error {
	var params, detail map[string]any
	var requestID, rawQuestions any
	var kind, title string
	canApprove := true
	if r.provider == "Codex" {
		params = object(event["params"])
		requestID = event["id"]
		if params == nil || r.sessionID == "" || str(params["threadId"]) != r.sessionID || r.turnID != "" && str(params["turnId"]) != r.turnID {
			return r.connection.RejectTool(event)
		}
		kind = map[string]string{"item/commandExecution/requestApproval": "command", "item/fileChange/requestApproval": "file", "item/permissions/requestApproval": "permissions", "item/tool/requestUserInput": "question"}[str(event["method"])]
		if kind == "" {
			if err := r.connection.RejectTool(event); err != nil {
				return err
			}
			return r.emit(map[string]any{"type": "notice", "message": "This provider request is not supported in Camp yet"})
		}
		detail, _ = cloneObject(params)
		if kind == "file" {
			detail["changes"] = r.items[str(params["itemId"])]
			canApprove = r.items[str(params["itemId"])] != nil
		}
		if kind == "question" {
			rawQuestions = params["questions"]
		}
		if kind == "permissions" {
			_, canApprove = params["permissions"].(map[string]any)
		}
		title = map[string]string{"command": "Approve command", "file": "Approve file changes", "permissions": "Approve access for this turn", "question": "Input requested"}[kind]
	} else {
		params = object(event["request"])
		requestID = event["request_id"]
		if str(params["subtype"]) != "can_use_tool" || str(requestID) == "" {
			return r.connection.Write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": requestID, "error": "Unsupported Camp control request"}})
		}
		tool := str(params["tool_name"])
		kind, title = "tool", "Approve "+tool
		if tool == "AskUserQuestion" {
			kind, title = "question", "Input requested"
			rawQuestions = object(params["input"])["questions"]
		}
		_, canApprove = params["input"].(map[string]any)
		detail = map[string]any{"tool": tool, "input": params["input"], "reason": params["decision_reason"]}
	}
	for _, pending := range r.pending {
		if sameID(pending.requestID, requestID) {
			return nil
		}
	}
	pending := &pendingApproval{requestID: requestID, kind: kind, canApprove: canApprove}
	detailJSON, err := json.MarshalIndent(detail, "", "  ")
	if err != nil || len(detailJSON) > 64<<10 || len(title) > 512 || len(r.pending) >= 16 {
		return r.declineInvalid(pending)
	}
	pending.params, err = cloneObject(params)
	if err != nil {
		return r.declineInvalid(pending)
	}
	pending.questions = []approvalQuestion{}
	if kind == "question" {
		pending.questions, err = normalizeApprovalQuestions(rawQuestions, r.provider == "Claude")
		if err != nil {
			return r.declineInvalid(pending)
		}
	}
	pending.decisions = []approvalDecision{}
	if r.provider == "Codex" && kind == "command" {
		pending.decisions, err = commandApprovalDecisions(pending.params["availableDecisions"])
		if err != nil {
			return r.declineInvalid(pending)
		}
		pending.canApprove = false
		for _, decision := range pending.decisions {
			pending.canApprove = pending.canApprove || decision.Allows
		}
	}
	key := uuid.NewString()
	r.pending[key] = pending
	acceptLabel := "Allow once"
	if kind == "question" {
		acceptLabel = "Submit answers"
	} else if kind == "permissions" {
		acceptLabel = "Allow for this turn"
	}
	return r.emit(map[string]any{"type": "approval", "id": key, "kind": kind, "title": title, "detail": string(detailJSON),
		"questions": pending.questions, "canApprove": pending.canApprove, "decisions": pending.decisions, "acceptLabel": acceptLabel})
}

func normalizeApprovalQuestions(raw any, claude bool) ([]approvalQuestion, error) {
	questions, ok := raw.([]any)
	if !ok || len(questions) == 0 || len(questions) > 8 {
		return nil, fmt.Errorf("Invalid questions")
	}
	result := make([]approvalQuestion, 0, len(questions))
	seen := make(map[string]bool)
	seenPrompts := make(map[string]bool)
	for index, value := range questions {
		question, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Invalid question")
		}
		id := str(question["id"])
		if _, present := question["id"]; !present {
			id = strconv.Itoa(index)
		}
		prompt := str(question["question"])
		if id == "" || seen[id] || strings.TrimSpace(prompt) == "" || len(prompt) > 8192 || claude && seenPrompts[prompt] {
			return nil, fmt.Errorf("Invalid question identity or prompt")
		}
		seen[id] = true
		seenPrompts[prompt] = true
		q := approvalQuestion{ID: id, Header: str(question["header"]), Prompt: str(question["question"]), Options: []string{},
			Multiple: question["multiSelect"] == true, Secret: question["isSecret"] == true, AllowText: claude}
		if isOther, present := question["isOther"]; present {
			q.AllowText = isOther == true
		}
		if options, exists := question["options"]; exists && options != nil {
			list, ok := options.([]any)
			if !ok || len(list) > 16 {
				return nil, fmt.Errorf("Invalid question options")
			}
			for _, option := range list {
				label := str(object(option)["label"])
				if strings.TrimSpace(label) == "" {
					return nil, fmt.Errorf("Invalid question option")
				}
				q.Options = append(q.Options, label)
			}
		}
		result = append(result, q)
	}
	return result, nil
}

func commandApprovalDecisions(raw any) ([]approvalDecision, error) {
	if raw == nil {
		raw = []any{"accept", "decline"}
	}
	offered, ok := raw.([]any)
	if !ok || len(offered) > 16 {
		return nil, fmt.Errorf("Invalid approval decisions")
	}
	result := []approvalDecision{}
	seen := make(map[string]bool)
	labels := map[string]string{"accept": "Allow once", "acceptForSession": "Allow for this session", "decline": "Decline", "cancel": "Decline and stop"}
	for _, value := range offered {
		if choice, ok := value.(string); ok && labels[choice] != "" && !seen[choice] {
			result = append(result, approvalDecision{ID: choice, Label: labels[choice], Allows: choice == "accept" || choice == "acceptForSession", Value: choice})
			seen[choice] = true
		} else if choice, ok := value.(map[string]any); ok && len(choice) == 1 {
			if _, ok := choice["acceptWithExecpolicyAmendment"].(map[string]any); ok {
				result = append(result, approvalDecision{ID: uuid.NewString(), Label: "Save command rule and allow", Allows: true, Value: choice})
			} else if action := str(object(object(choice["applyNetworkPolicyAmendment"])["network_policy_amendment"])["action"]); action == "allow" || action == "deny" {
				result = append(result, approvalDecision{ID: uuid.NewString(), Label: "Save network " + action + " rule", Allows: action == "allow", Value: choice})
			}
		}
	}
	return result, nil
}

func (r *approvalRelay) declineInvalid(pending *pendingApproval) error {
	if err := r.deny(pending); err != nil {
		return err
	}
	return r.emit(map[string]any{"type": "notice", "message": "A provider approval was invalid or exceeded the size or pending-request limit and was declined"})
}

func (r *approvalRelay) deny(pending *pendingApproval) error {
	if r.provider == "Codex" {
		result := map[string]any{"decision": "decline"}
		if pending.kind == "question" {
			result = map[string]any{"answers": map[string]any{}}
		} else if pending.kind == "permissions" {
			result = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
		}
		return r.connection.Write(map[string]any{"id": pending.requestID, "result": result})
	}
	return r.connection.Write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": pending.requestID,
		"response": map[string]any{"behavior": "deny", "message": "Declined in Capybara Camp"}}})
}

func (r *approvalRelay) Resolve(control map[string]any) error {
	key := str(control["id"])
	reject := func(message string) error {
		return r.emit(map[string]any{"type": "approvalRejected", "requestID": key, "message": message})
	}
	pending := r.pending[key]
	if pending == nil {
		return reject("This approval is no longer pending")
	}
	allow := control["allow"] == true
	var chosen *approvalDecision
	wanted := str(control["decision"])
	if control["decision"] != nil || len(pending.decisions) > 0 {
		if control["decision"] == nil {
			wanted = "decline"
			if allow {
				wanted = "accept"
			}
		}
		for index := range pending.decisions {
			decision := &pending.decisions[index]
			if decision.ID == wanted && decision.Allows == allow {
				chosen = decision
				break
			}
		}
		if chosen == nil {
			return reject("Choose an offered approval decision")
		}
	}
	if allow && !pending.canApprove {
		return reject("The full action cannot be approved from this preview")
	}
	answers := make(map[string][]string)
	if allow && pending.kind == "question" {
		provided, ok := control["answers"].(map[string]any)
		if !ok || len(provided) != len(pending.questions) {
			return reject("Answer every question before submitting")
		}
		for _, question := range pending.questions {
			values, ok := provided[question.ID].([]any)
			if !ok || len(values) == 0 || len(values) > 8 || !question.Multiple && len(values) != 1 {
				return reject("Choose a valid answer for each question")
			}
			for _, value := range values {
				text, ok := value.(string)
				if !ok || strings.TrimSpace(text) == "" || len(text) > 4096 {
					return reject("Choose a valid answer for each question")
				}
				found := len(question.Options) == 0 || question.AllowText
				for _, option := range question.Options {
					found = found || option == text
				}
				if !found {
					return reject("Choose a valid answer for each question")
				}
				answers[question.ID] = append(answers[question.ID], text)
			}
		}
	}
	var err error
	switch {
	case chosen != nil:
		err = r.connection.Write(map[string]any{"id": pending.requestID, "result": map[string]any{"decision": chosen.Value}})
	case !allow:
		err = r.deny(pending)
	case r.provider == "Codex":
		result := map[string]any{"decision": "accept"}
		if pending.kind == "permissions" {
			result = map[string]any{"permissions": pending.params["permissions"], "scope": "turn"}
		} else if pending.kind == "question" {
			value := make(map[string]any)
			for id, selected := range answers {
				value[id] = map[string]any{"answers": selected}
			}
			result = map[string]any{"answers": value}
		}
		err = r.connection.Write(map[string]any{"id": pending.requestID, "result": result})
	default:
		input, _ := cloneObject(object(pending.params["input"]))
		if pending.kind == "question" {
			selected := make(map[string]any)
			for _, question := range pending.questions {
				selected[question.Prompt] = strings.Join(answers[question.ID], ", ")
			}
			input["answers"] = selected
		}
		err = r.connection.Write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": pending.requestID,
			"response": map[string]any{"behavior": "allow", "updatedInput": input}}})
	}
	if err != nil {
		return err // A failed/partial write must not report success or retry itself.
	}
	delete(r.pending, key)
	return r.emit(map[string]any{"type": "approvalResolved", "requestID": key})
}
