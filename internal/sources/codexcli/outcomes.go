package codexcli

import (
	"encoding/json"
	"strings"

	"github.com/brandonbosch/chatstrata/v2/internal/sources/srcutil"
)

type toolOutcome struct {
	status   string
	exitCode *int
	failed   bool
	errMsg   string
}

type outcomeTracker struct {
	knownCallIDs map[string]bool
	byCallID     map[string][]toolOutcome
	pending      []toolOutcome
}

func newOutcomeTracker(events []srcutil.Event) *outcomeTracker {
	known := map[string]bool{}
	for _, ev := range events {
		if ev.Fields["type"] != "response_item" {
			continue
		}
		p := srcutil.Map(ev.Fields, "payload")
		switch p["type"] {
		case "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
			if id, ok := p["call_id"].(string); ok && id != "" {
				known[id] = true
			}
		}
	}
	return &outcomeTracker{
		knownCallIDs: known,
		byCallID:     map[string][]toolOutcome{},
	}
}

func (t *outcomeTracker) observeEventMsg(p map[string]any) {
	evType, _ := p["type"].(string)
	switch evType {
	case "user_message", "task_started", "task_complete", "turn_aborted":
		t.pending = nil
	case "exec_command_end":
		code := intField(p["exit_code"])
		status, _ := p["status"].(string)
		failed := isFailureStatus(status) || (code != nil && *code != 0)
		if status == "" && code != nil {
			status = statusFromFailed(failed)
		}
		if status == "" && code == nil {
			return
		}
		t.record(stringField(p["call_id"]), toolOutcome{
			status:   status,
			exitCode: code,
			failed:   failed,
		})
	case "patch_apply_end":
		status, _ := p["status"].(string)
		success, hasSuccess := p["success"].(bool)
		if !hasSuccess && status == "" {
			return
		}
		failed := (hasSuccess && !success) || isFailureStatus(status)
		if status == "" {
			status = statusFromFailed(failed)
		}
		t.record(stringField(p["call_id"]), toolOutcome{
			status: status,
			failed: failed,
		})
	case "item_completed":
		item := srcutil.Map(p, "item")
		itemType, _ := item["type"].(string)
		switch itemType {
		case "CommandExecution", "FileChange", "McpToolCall", "CollabAgentToolCall", "Extension":
		default:
			return
		}
		status, _ := item["status"].(string)
		code := intField(item["exit_code"])
		errMsg := itemErrorMessage(item["error"])
		if status == "" && code == nil && errMsg == "" {
			return
		}
		failed := isFailureStatus(status) || (code != nil && *code != 0) || errMsg != ""
		if status == "" {
			status = statusFromFailed(failed)
		}
		itemID := stringField(item["id"])
		out := toolOutcome{
			status:   status,
			exitCode: code,
			failed:   failed,
			errMsg:   errMsg,
		}
		if itemID != "" && t.knownCallIDs[itemID] {
			t.byCallID[itemID] = append(t.byCallID[itemID], out)
		} else {
			t.pending = append(t.pending, out)
		}
	}
}

func (t *outcomeTracker) record(callID string, out toolOutcome) {
	if callID != "" {
		t.byCallID[callID] = append(t.byCallID[callID], out)
		return
	}
	t.pending = append(t.pending, out)
}

func (t *outcomeTracker) consume(callID *string, outputText *string) map[string]any {
	var matched []toolOutcome
	if callID != nil && *callID != "" && len(t.byCallID[*callID]) > 0 {
		matched = t.byCallID[*callID]
		delete(t.byCallID, *callID)
	} else if len(t.pending) > 0 {
		matched = t.pending
		t.pending = nil
	}

	payload := mergeOutcomes(matched)
	if outputText != nil {
		payload = enrichFromOutputText(payload, *outputText)
	}
	return payload
}

func mergeOutcomes(outcomes []toolOutcome) map[string]any {
	if len(outcomes) == 0 {
		return nil
	}
	var exitCodes []int
	var failed bool
	var status string
	var errMsg string
	for _, o := range outcomes {
		if o.exitCode != nil {
			exitCodes = append(exitCodes, *o.exitCode)
		}
		if o.failed {
			failed = true
			if o.status != "" {
				status = o.status
			}
		} else if status == "" && o.status != "" {
			status = o.status
		}
		if errMsg == "" && o.errMsg != "" {
			errMsg = o.errMsg
		}
	}
	if failed && !isFailureStatus(status) {
		status = "failed"
	}
	if status == "" {
		status = statusFromFailed(failed)
	}
	payload := map[string]any{
		"status":   status,
		"is_error": failed,
	}
	if len(exitCodes) == 1 {
		payload["exit_code"] = exitCodes[0]
	} else if len(exitCodes) > 1 {
		code := 0
		for _, c := range exitCodes {
			if c != 0 {
				code = c
			}
		}
		payload["exit_code"] = code
		payload["exit_codes"] = exitCodes
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	return payload
}

func enrichFromOutputText(payload map[string]any, text string) map[string]any {
	if payload == nil || payload["exit_code"] == nil {
		if strings.HasPrefix(text, "{") {
			if obj, ok := srcutil.DecodeObject([]byte(text)); ok {
				if meta := srcutil.Map(obj, "metadata"); meta != nil {
					if code := intField(meta["exit_code"]); code != nil {
						if payload == nil {
							payload = map[string]any{}
						}
						payload["exit_code"] = *code
						if _, hasErr := payload["is_error"]; !hasErr {
							payload["is_error"] = *code != 0
						}
						if _, hasStatus := payload["status"]; !hasStatus {
							payload["status"] = statusFromFailed(*code != 0)
						}
					}
				}
			}
		}
	}
	if payload == nil && (text == "Script failed" || strings.HasPrefix(text, "Script failed\n")) {
		payload = map[string]any{
			"status":   "failed",
			"is_error": true,
		}
	}
	return payload
}

func isFailureStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "declined", "cancelled", "canceled", "interrupted", "timed_out", "timeout":
		return true
	default:
		return false
	}
}

func statusFromFailed(failed bool) string {
	if failed {
		return "failed"
	}
	return "completed"
}

func stringField(v any) string {
	s, _ := v.(string)
	return s
}

func intField(v any) *int {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			n := int(i)
			return &n
		}
	case float64:
		n := int(x)
		return &n
	case int:
		n := x
		return &n
	case int64:
		n := int(x)
		return &n
	}
	return nil
}

func itemErrorMessage(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]any:
		if s, ok := x["message"].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
