package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/cursor"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCursorMessagesFromChatFlattensTextParts(t *testing.T) {
	parts, err := json.Marshal([]map[string]string{
		{"type": "text", "text": "hello "},
		{"type": "text", "text": "world"},
	})
	require.NoError(t, err)

	got := buildCursorAgentRunRequest("m", []apicompat.ChatMessage{
		{Role: "user", Content: parts},
		{Role: "assistant", Content: json.RawMessage(`"prior"`)},
	}, nil, "")
	require.Equal(t, "hello world", got.Messages[0].Content)
	require.Equal(t, "prior", got.Messages[1].Content)
}

func TestClaudeUsageFromCursorMapsCacheAndReasoning(t *testing.T) {
	got := claudeUsageFromCursor(cursor.TokenUsage{
		InputTokens:      10,
		OutputTokens:     4,
		CacheReadTokens:  6,
		CacheWriteTokens: 2,
		ReasoningTokens:  3,
	})
	require.Equal(t, 10, got.InputTokens)
	require.Equal(t, 4, got.OutputTokens)
	require.Equal(t, 6, got.CacheReadInputTokens)
	require.Equal(t, 2, got.CacheCreationInputTokens)

	got = claudeUsageFromCursor(cursor.TokenUsage{ReasoningTokens: 8})
	require.Equal(t, 8, got.OutputTokens)
}

func TestChatUsageFromCursorUsesGrossPromptTokens(t *testing.T) {
	got := chatUsageFromCursor(cursor.TokenUsage{
		InputTokens:      10,
		OutputTokens:     4,
		CacheReadTokens:  6,
		CacheWriteTokens: 2,
		ReasoningTokens:  3,
	})
	require.NotNil(t, got)
	require.Equal(t, 18, got.PromptTokens)
	require.Equal(t, 4, got.CompletionTokens)
	require.Equal(t, 22, got.TotalTokens)
	require.Equal(t, 6, got.PromptTokensDetails.CachedTokens)
	require.Equal(t, 2, got.PromptTokensDetails.CacheWriteTokens)
	require.Equal(t, 3, got.CompletionTokensDetails.ReasoningTokens)
	require.Nil(t, chatUsageFromCursor(cursor.TokenUsage{}))
}

func TestCursorNonStreamResponseRecordsTurnEndedUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frames := encodeCursorAssistantFrames(t, "Hi", 12, 7)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	result, err := NewCursorGatewayService(nil, nil).nonStreamResponse(c, bytes.NewReader(frames), "grok-4.6", nil, time.Now())
	require.NoError(t, err)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *apicompat.ChatUsage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &parsed))
	require.Equal(t, "Hi", parsed.Choices[0].Message.Content)
	require.Equal(t, 12, parsed.Usage.PromptTokens)
	require.Equal(t, 7, parsed.Usage.CompletionTokens)
}

func TestCursorNonStreamResponseIncludesReasoningContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frames := encodeCursorThinkingAndTextFrames(t, "plan first", "Hi", 3, 2)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	_, err := NewCursorGatewayService(nil, nil).nonStreamResponse(c, bytes.NewReader(frames), "claude-opus-5", nil, time.Now())
	require.NoError(t, err)

	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &parsed))
	require.Equal(t, "Hi", parsed.Choices[0].Message.Content)
	require.Equal(t, "plan first", parsed.Choices[0].Message.ReasoningContent)
}

func TestCursorStreamResponseForwardsThinking(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frames := encodeCursorThinkingAndTextFrames(t, "plan first", "Hi", 3, 2)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	_, err := NewCursorGatewayService(nil, nil).streamResponse(c, bytes.NewReader(frames), "claude-opus-5", nil, time.Now(), false)
	require.NoError(t, err)

	body := rec.Body.String()
	require.Contains(t, body, `"reasoning_content":"plan first"`)
	require.Contains(t, body, `"content":"Hi"`)
}

func TestCursorForwardAsChatCompletionsAcceptsContentPartsArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := cursorAccountWithFreshToken(21)
	svc := NewCursorGatewayService(nil, nil)
	var contents []string
	svc.streamChat = func(_ context.Context, _ cursor.Credentials, req cursor.AgentRunRequest) (*http.Response, error) {
		for _, m := range req.Messages {
			contents = append(contents, m.Content)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	}

	body := []byte(`{"model":"claude-opus-5","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]},{"role":"assistant","content":null},{"role":"user","content":"plain"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body)
	require.NoError(t, err)
	require.Equal(t, []string{"hello world", "", "plain"}, contents)
}

func TestCursorStreamResponseSurfacesErrorAfterPartialOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frames := encodeCursorTextFrames(t, "partial answer")
	frames = append(frames, encodeCursorConnectErrorFrame(t, `{"code":"unavailable","message":"upstream dropped"}`)...)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	_, err := NewCursorGatewayService(nil, nil).streamResponse(c, bytes.NewReader(frames), "grok-4.6", nil, time.Now(), true)
	require.NoError(t, err)

	body := rec.Body.String()
	require.Contains(t, body, `"content":"partial answer"`)
	require.Contains(t, body, `"error"`)
	require.Contains(t, body, "upstream dropped")
	require.NotContains(t, body, `"finish_reason":"stop"`)
}

func TestCursorStreamAnthropicSurfacesErrorAfterThinkingOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frames := encodeCursorThinkingFrames(t, "thinking away")
	frames = append(frames, encodeCursorConnectErrorFrame(t, `{"code":"unavailable","message":"upstream dropped"}`)...)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	_, err := NewCursorGatewayService(nil, nil).streamCursorAsAnthropic(c, bytes.NewReader(frames), "claude-opus-5", nil, time.Now())
	require.NoError(t, err)

	body := rec.Body.String()
	require.Contains(t, body, "event: error")
	require.Contains(t, body, "upstream dropped")
	require.NotContains(t, body, "message_stop")
}

// encodeCursorConnectErrorFrame builds a Connect end-stream frame (flags 0x02)
// carrying a JSON error payload, as Cursor sends on failed turns.
func encodeCursorConnectErrorFrame(t *testing.T, payload string) []byte {
	t.Helper()
	out := make([]byte, 5+len(payload))
	out[0] = cursor.FrameFlagEndStream
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

func encodeCursorTextFrames(t *testing.T, text string) []byte {
	t.Helper()
	var delta cursor.ProtobufWriter
	delta.String(1, text)
	var update cursor.ProtobufWriter
	update.Bytes(1, delta.Result())
	var server cursor.ProtobufWriter
	server.Bytes(1, update.Result())
	frame, err := cursor.EncodeFrame(server.Result(), false)
	require.NoError(t, err)
	return frame
}

func encodeCursorThinkingFrames(t *testing.T, thinking string) []byte {
	t.Helper()
	var delta cursor.ProtobufWriter
	delta.String(1, thinking)
	var update cursor.ProtobufWriter
	update.Bytes(4, delta.Result())
	var server cursor.ProtobufWriter
	server.Bytes(1, update.Result())
	frame, err := cursor.EncodeFrame(server.Result(), false)
	require.NoError(t, err)
	return frame
}

func encodeCursorAssistantFrames(t *testing.T, text string, inputTokens, outputTokens int) []byte {
	t.Helper()
	return encodeCursorThinkingAndTextFrames(t, "", text, inputTokens, outputTokens)
}

func encodeCursorThinkingAndTextFrames(t *testing.T, thinking, text string, inputTokens, outputTokens int) []byte {
	t.Helper()
	var out []byte
	if thinking != "" {
		var delta cursor.ProtobufWriter
		delta.String(1, thinking)
		var update cursor.ProtobufWriter
		update.Bytes(4, delta.Result())
		var server cursor.ProtobufWriter
		server.Bytes(1, update.Result())
		frame, err := cursor.EncodeFrame(server.Result(), false)
		require.NoError(t, err)
		out = append(out, frame...)
	}
	if text != "" {
		var delta cursor.ProtobufWriter
		delta.String(1, text)
		var textUpdate cursor.ProtobufWriter
		textUpdate.Bytes(1, delta.Result())
		var textServer cursor.ProtobufWriter
		textServer.Bytes(1, textUpdate.Result())
		textFrame, err := cursor.EncodeFrame(textServer.Result(), false)
		require.NoError(t, err)
		out = append(out, textFrame...)
	}

	var ended cursor.ProtobufWriter
	ended.Varint(1, inputTokens)
	ended.Varint(2, outputTokens)
	var endUpdate cursor.ProtobufWriter
	endUpdate.Bytes(14, ended.Result())
	var endServer cursor.ProtobufWriter
	endServer.Bytes(1, endUpdate.Result())
	endFrame, err := cursor.EncodeFrame(endServer.Result(), false)
	require.NoError(t, err)
	return append(out, endFrame...)
}

func TestForwardAsResponsesRemoteCompactionEmitsOneItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := cursorAccountWithFreshToken(44)
	svc := NewCursorGatewayService(nil, nil)
	svc.streamChat = func(context.Context, cursor.Credentials, cursor.AgentRunRequest) (*http.Response, error) {
		t.Fatal("compaction must not call Cursor")
		return nil, nil
	}

	body := []byte(`{
		"model": "grok-4.7-high-fast",
		"stream": true,
		"tools": [{"type":"function","name":"shell","parameters":{"type":"object"}}],
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},
			{"type":"compaction_trigger"}
		]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	_, err := svc.ForwardAsResponses(context.Background(), c, account, body)
	require.NoError(t, err)

	var compactionItems, otherItems int
	var completedOutput []map[string]any
	sawCompleted := false
	for _, block := range bytes.Split(bytes.TrimSpace(rec.Body.Bytes()), []byte("\n\n")) {
		var event, data string
		for _, line := range bytes.Split(block, []byte("\n")) {
			switch {
			case bytes.HasPrefix(line, []byte("event: ")):
				event = string(bytes.TrimPrefix(line, []byte("event: ")))
			case bytes.HasPrefix(line, []byte("data: ")):
				data = string(bytes.TrimPrefix(line, []byte("data: ")))
			}
		}
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &payload))
		switch event {
		case "response.output_item.done":
			item, _ := payload["item"].(map[string]any)
			if item["type"] == "compaction" {
				compactionItems++
				enc, _ := item["encrypted_content"].(string)
				require.Contains(t, enc, "fix the login bug")
				summary, _ := item["summary"].([]any)
				require.Len(t, summary, 1)
			} else {
				otherItems++
			}
		case "response.completed":
			sawCompleted = true
			response, _ := payload["response"].(map[string]any)
			rawOutput, _ := response["output"].([]any)
			require.Len(t, rawOutput, 1)
			for _, raw := range rawOutput {
				item, _ := raw.(map[string]any)
				completedOutput = append(completedOutput, item)
			}
		}
	}
	require.True(t, sawCompleted)
	require.Equal(t, 1, compactionItems)
	require.Zero(t, otherItems)
	require.Equal(t, "compaction", completedOutput[0]["type"])
	require.NotContains(t, rec.Body.String(), `"type":"message"`)
	require.NotContains(t, rec.Body.String(), `"type":"reasoning"`)
}

func TestForwardAsResponsesCarriesCompactionHandoff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := cursorAccountWithFreshToken(45)
	svc := NewCursorGatewayService(nil, nil)
	var captured cursor.AgentRunRequest
	svc.streamChat = func(_ context.Context, _ cursor.Credentials, req cursor.AgentRunRequest) (*http.Response, error) {
		captured = req
		body := encodeCursorAssistantFrames(t, "continued", 3, 1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
	}

	body := []byte(`{
		"model": "grok-4.7-high-fast",
		"stream": true,
		"input": [
			{"type":"compaction","encrypted_content":"opaqueblob","summary":[{"type":"summary_text","text":"login fix is half done"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	_, err := svc.ForwardAsResponses(context.Background(), c, account, body)
	require.NoError(t, err)

	var upstream string
	for _, message := range captured.Messages {
		upstream += message.Content + "\n"
	}
	require.Contains(t, upstream, "login fix is half done")
	require.Contains(t, upstream, "continue")
	require.NotContains(t, upstream, "opaqueblob")
	require.NotContains(t, upstream, "CONTEXT CHECKPOINT COMPACTION")
}
