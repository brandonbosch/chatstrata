package codexcli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/model"
)

func parseLines(t *testing.T, lines []string) *model.Conversation {
	t.Helper()
	conv, err := (Source{}).Parse(
		model.Handle{SourceNativeID: "sess-1", Path: "/tmp/rollout-sess-1.jsonl"},
		[]byte(strings.Join(lines, "\n")+"\n"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

func toolResultPayloads(conv *model.Conversation) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, m := range conv.Messages {
		for _, b := range m.Blocks {
			if b.Type == model.BlockToolResult && b.ToolUseID != nil {
				out[*b.ToolUseID] = b.Payload
			}
		}
	}
	return out
}

func TestParseAttachesItemCompletedExitCodesAndFailures(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-10-20T10:00:00Z","type":"session_meta","payload":{"cwd":"/work/repo"}}`,
		`{"timestamp":"2026-10-20T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"run tests"}}`,
		// 1. Exec call whose JS wrapper says "Script completed" while item_completed reports exit_code=128 failed.
		`{"timestamp":"2026-10-20T10:00:02Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call_fail","name":"exec","input":"await tools.exec_command({cmd:'git status'})"}}`,
		`{"timestamp":"2026-10-20T10:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-1","status":"failed","exit_code":128}}}`,
		`{"timestamp":"2026-10-20T10:00:03Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_fail","output":[{"type":"input_text","text":"Script completed\nWall time 0.1 seconds\nOutput:\n"},{"type":"input_text","text":"fatal: not a git repository"}]}}`,
		// 2. Exec call with two sub-commands (0 then 1) records exit_codes and non-zero exit_code.
		`{"timestamp":"2026-10-20T10:00:04Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call_multi","name":"exec","input":"two cmds"}}`,
		`{"timestamp":"2026-10-20T10:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-2","status":"completed","exit_code":0}}}`,
		`{"timestamp":"2026-10-20T10:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-3","status":"failed","exit_code":2}}}`,
		`{"timestamp":"2026-10-20T10:00:05Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_multi","output":"Script completed\nWall time 0.2 seconds\nOutput:\n"}}`,
		// 3. Passing exec call with Extension web.search (no status) followed by CommandExecution exit_code=0.
		`{"timestamp":"2026-10-20T10:00:06Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call_ok","name":"exec","input":"pwd"}}`,
		`{"timestamp":"2026-10-20T10:00:06Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"Extension","kind":"web.search","id":"exec-4"}}}`,
		`{"timestamp":"2026-10-20T10:00:07Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-5","status":"completed","exit_code":0}}}`,
		`{"timestamp":"2026-10-20T10:00:07Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_ok","output":"Script completed\nWall time 0.0 seconds\nOutput:\n/work/repo"}}`,
		// 4. Direct call_id match on McpToolCall failure with error message.
		`{"timestamp":"2026-10-20T10:00:08Z","type":"response_item","payload":{"type":"function_call","call_id":"call_mcp","name":"query","arguments":"{}"}}`,
		`{"timestamp":"2026-10-20T10:00:09Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"call_mcp","status":"failed","error":{"message":"MCP tool call requires approval, but approval policy is never"}}}}`,
		`{"timestamp":"2026-10-20T10:00:09Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_mcp","output":"denied"}}`,
		// 5. JS script failure with no item_completed event.
		`{"timestamp":"2026-10-20T10:00:10Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call_script_err","name":"exec","input":"bad patch"}}`,
		`{"timestamp":"2026-10-20T10:00:11Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_script_err","output":[{"type":"input_text","text":"Script failed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\napply_patch verification failed"}]}}`,
	}

	got := toolResultPayloads(parseLines(t, lines))

	wantFail := map[string]any{"status": "failed", "is_error": true, "exit_code": 128}
	if !reflect.DeepEqual(got["call_fail"], wantFail) {
		t.Errorf("call_fail payload = %#v, want %#v", got["call_fail"], wantFail)
	}

	wantMulti := map[string]any{"status": "failed", "is_error": true, "exit_code": 2, "exit_codes": []int{0, 2}}
	if !reflect.DeepEqual(got["call_multi"], wantMulti) {
		t.Errorf("call_multi payload = %#v, want %#v", got["call_multi"], wantMulti)
	}

	wantOK := map[string]any{"status": "completed", "is_error": false, "exit_code": 0}
	if !reflect.DeepEqual(got["call_ok"], wantOK) {
		t.Errorf("call_ok payload = %#v, want %#v", got["call_ok"], wantOK)
	}

	wantMCP := map[string]any{
		"status":   "failed",
		"is_error": true,
		"error":    "MCP tool call requires approval, but approval policy is never",
	}
	if !reflect.DeepEqual(got["call_mcp"], wantMCP) {
		t.Errorf("call_mcp payload = %#v, want %#v", got["call_mcp"], wantMCP)
	}

	wantScriptErr := map[string]any{"status": "failed", "is_error": true}
	if !reflect.DeepEqual(got["call_script_err"], wantScriptErr) {
		t.Errorf("call_script_err payload = %#v, want %#v", got["call_script_err"], wantScriptErr)
	}
}

func TestParseLegacyExecPatchAndTurnBoundaries(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-10-20T10:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`,
		// Unconsumed item_completed before task_complete must NOT bleed into the next turn.
		`{"timestamp":"2026-10-20T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-orphan","status":"failed","exit_code":99}}}`,
		`{"timestamp":"2026-10-20T10:00:02Z","type":"event_msg","payload":{"type":"task_complete"}}`,
		`{"timestamp":"2026-10-20T10:00:03Z","type":"event_msg","payload":{"type":"task_started"}}`,
		// Next turn has a tool output with no item_completed -> payload stays nil.
		`{"timestamp":"2026-10-20T10:00:04Z","type":"response_item","payload":{"type":"function_call","call_id":"call_clean","name":"wait","arguments":"{}"}}`,
		`{"timestamp":"2026-10-20T10:00:05Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_clean","output":"{\"timed_out\":false}"}}`,
		// Legacy exec_command_end and patch_apply_end match by call_id.
		`{"timestamp":"2026-10-20T10:00:06Z","type":"response_item","payload":{"type":"function_call","call_id":"call_exec","name":"exec_command","arguments":"{\"cmd\":\"false\"}"}}`,
		`{"timestamp":"2026-10-20T10:00:07Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"call_exec","exit_code":1,"status":"failed"}}`,
		`{"timestamp":"2026-10-20T10:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_exec","output":""}}`,
		`{"timestamp":"2026-10-20T10:00:08Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","input":"patch"}}`,
		`{"timestamp":"2026-10-20T10:00:09Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"call_patch","success":false}}`,
		`{"timestamp":"2026-10-20T10:00:09Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_patch","output":"{\"output\":\"failed\",\"metadata\":{\"exit_code\":1}}"}}`,
	}

	got := toolResultPayloads(parseLines(t, lines))

	if got["call_clean"] != nil {
		t.Errorf("orphan item bled across turn boundary: got %#v, want nil", got["call_clean"])
	}
	wantExec := map[string]any{"status": "failed", "is_error": true, "exit_code": 1}
	if !reflect.DeepEqual(got["call_exec"], wantExec) {
		t.Errorf("call_exec payload = %#v, want %#v", got["call_exec"], wantExec)
	}
	wantPatch := map[string]any{"status": "failed", "is_error": true, "exit_code": 1}
	if !reflect.DeepEqual(got["call_patch"], wantPatch) {
		t.Errorf("call_patch payload = %#v, want %#v", got["call_patch"], wantPatch)
	}
}
