package imsg

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestRetryUnsafeOnlyForSendsThatMayHaveGoneOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"rewritten GUID", &Error{Code: -32001, Message: "Delivery outcome unknown", Data: json.RawMessage(
			`{"retry_safe":false,"disposition":"may_have_completed","transport":"bridge_v2","operation":"send-message","detail":"Do not retry this attempt."}`)}, true},
		{"wrapped", fmt.Errorf("send: %w", &Error{Code: -32001, Data: json.RawMessage(`{"retry_safe":false}`)}), true},
		{"refused before dispatch", &Error{Code: -32603, Message: "Delivery failed before dispatch", Data: json.RawMessage(
			`{"retry_safe":true,"disposition":"not_started"}`)}, false},
		{"no delivery data", &Error{Code: -32602, Message: "Invalid params", Data: json.RawMessage(`"chat_guid is required"`)}, false},
		{"no data", &Error{Code: -32603, Message: "Internal error"}, false},
		{"not imsg", errors.New("broken pipe"), false},
		{"none", nil, false},
	} {
		if got := RetryUnsafe(tc.err); got != tc.want {
			t.Errorf("%s: RetryUnsafe = %v; want %v", tc.name, got, tc.want)
		}
	}
}
