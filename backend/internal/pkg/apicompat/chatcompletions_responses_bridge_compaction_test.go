package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToChatCompletionsRequest_CompactionSummaryBecomesUserMessage(t *testing.T) {
	req := &ResponsesRequest{
		Model: "grok-4.7-high-fast",
		Input: json.RawMessage(`[
			{"type":"compaction","encrypted_content":"opaqueblob","summary":[{"type":"summary_text","text":"login fix is half done"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 2)
	require.Equal(t, "user", out.Messages[0].Role)
	require.Contains(t, string(out.Messages[0].Content), "login fix is half done")
	require.NotContains(t, string(out.Messages[0].Content), "opaqueblob")
	require.JSONEq(t, `"continue"`, string(out.Messages[1].Content))
}

func TestResponsesToChatCompletionsRequest_CompactionProseEncryptedContent(t *testing.T) {
	req := &ResponsesRequest{
		Model: "grok-4.7-high-fast",
		Input: json.RawMessage(`[
			{"type":"compaction","encrypted_content":"handoff says keep going"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	require.Contains(t, string(out.Messages[0].Content), "handoff says keep going")
}

func TestResponsesToChatCompletionsRequest_CompactionOpaqueBlobIsSkipped(t *testing.T) {
	req := &ResponsesRequest{
		Model: "grok-4.7-high-fast",
		Input: json.RawMessage(`[
			{"type":"compaction","encrypted_content":"opaqueblob"},
			{"type":"compaction_trigger"},
			{"type":"message","role":"user","content":"continue"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	require.JSONEq(t, `"continue"`, string(out.Messages[0].Content))
}
