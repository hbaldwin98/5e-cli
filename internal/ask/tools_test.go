package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/edition"
)

// toolCallServer answers chat/completions with one round of tool calls (when
// no role:"tool" message is present yet) and a plain answer thereafter, so a
// test can exercise the full round trip without a real backend.
func toolCallServer(t *testing.T, toolName string, args string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		ranTool := false
		for _, m := range req.Messages {
			if m.Role == "tool" {
				ranTool = true
			}
		}
		if !ranTool {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"tool_calls": []map[string]any{{
							"id": "call_1",
							"function": map[string]any{
								"name":      toolName,
								"arguments": args,
							},
						}},
					},
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "the tool answered"}}},
		})
	})
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		type row struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []row
		for i, s := range req.Input {
			data = append(data, row{Index: i, Embedding: keywordEmbed(s)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestRunToolLoop_executesToolThenReturnsAnswer(t *testing.T) {
	srv, calls := toolCallServer(t, "dice", `{"expression":"2d6+3"}`)
	var executed atomic.Int32
	cfg := Config{
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		AskModel:   "fake-ask",
		HTTPClient: srv.Client(),
		Tools:      []Tool{{Name: "dice", Description: "roll dice"}},
		ToolExecutor: func(_ context.Context, call ToolCall) (string, error) {
			executed.Add(1)
			if call.Name != "dice" {
				t.Fatalf("unexpected tool %q", call.Name)
			}
			return `{"total":7}`, nil
		},
	}.withDefaults()

	cli := newClient(cfg)
	answer, err := runToolLoop(context.Background(), cli, cfg, []Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "roll 2d6+3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "the tool answered" {
		t.Fatalf("answer: %q", answer)
	}
	if executed.Load() != 1 {
		t.Fatalf("executor calls: %d", executed.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two model round trips, got %d", calls.Load())
	}
}

func TestRunToolLoop_toolErrorIsReportedNotFatal(t *testing.T) {
	srv, _ := toolCallServer(t, "dice", `{"expression":"bogus"}`)
	cfg := Config{
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		AskModel:   "fake-ask",
		HTTPClient: srv.Client(),
		Tools:      []Tool{{Name: "dice"}},
		ToolExecutor: func(_ context.Context, call ToolCall) (string, error) {
			return "", fmt.Errorf("bad expression")
		},
	}.withDefaults()

	cli := newClient(cfg)
	answer, err := runToolLoop(context.Background(), cli, cfg, []Message{
		{Role: "user", Content: "roll bogus"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "the tool answered" {
		t.Fatalf("a tool error should still let the model answer, got %q", answer)
	}
}

func TestConverse_withToolsRunsTheLoop(t *testing.T) {
	st, cfg, _ := harness(t)
	defer st.Close()

	srv, calls := toolCallServer(t, "dice", `{"expression":"1d20"}`)
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = srv.Client()
	var executed atomic.Int32
	cfg.Tools = []Tool{{Name: "dice", Description: "roll dice"}}
	cfg.ToolExecutor = func(_ context.Context, call ToolCall) (string, error) {
		executed.Add(1)
		return `{"total":11}`, nil
	}

	res, err := Converse(context.Background(), st, cfg, Turn{Query: Query{Text: "roll initiative", Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "the tool answered" {
		t.Fatalf("answer: %q", res.Answer)
	}
	if executed.Load() != 1 {
		t.Fatalf("executor calls: %d", executed.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("model round trips: %d", calls.Load())
	}
}

func TestConverseStream_withToolsFlushesFinalAnswerOnce(t *testing.T) {
	st, cfg, _ := harness(t)
	defer st.Close()

	srv, _ := toolCallServer(t, "dice", `{"expression":"1d20"}`)
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = srv.Client()
	cfg.Tools = []Tool{{Name: "dice"}}
	cfg.ToolExecutor = func(_ context.Context, call ToolCall) (string, error) {
		return `{"total":11}`, nil
	}

	var deltas []string
	res, err := ConverseStream(context.Background(), st, cfg, Turn{Query: Query{Text: "roll initiative", Limit: 1}}, func(s string) error {
		deltas = append(deltas, s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "the tool answered" {
		t.Fatalf("answer: %q", res.Answer)
	}
	if len(deltas) != 1 || deltas[0] != "the tool answered" {
		t.Fatalf("expected the whole answer flushed once, got %+v", deltas)
	}
}

func TestConverse_withToolsReportsOnToolCall(t *testing.T) {
	st, cfg, _ := harness(t)
	defer st.Close()

	srv, _ := toolCallServer(t, "dice", `{"expression":"1d20"}`)
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = srv.Client()
	cfg.Tools = []Tool{{Name: "dice"}}
	cfg.ToolExecutor = func(_ context.Context, call ToolCall) (string, error) {
		return `{"total":11}`, nil
	}
	var reported []string
	cfg.OnToolCall = func(name string, _ json.RawMessage) {
		reported = append(reported, name)
	}

	if _, err := Converse(context.Background(), st, cfg, Turn{Query: Query{Text: "roll initiative", Limit: 1}}); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || reported[0] != "dice" {
		t.Fatalf("expected OnToolCall(\"dice\", ...) once, got %+v", reported)
	}
}

func TestRunToolLoop_boundsRounds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"tool_calls": []map[string]any{{
						"id":       "call_1",
						"function": map[string]any{"name": "dice", "arguments": `{"expression":"1d20"}`},
					}},
				},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	cfg := Config{
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		AskModel:   "fake-ask",
		HTTPClient: srv.Client(),
		Tools:      []Tool{{Name: "dice"}},
		ToolExecutor: func(_ context.Context, call ToolCall) (string, error) {
			return `{"total":11}`, nil
		},
	}.withDefaults()
	cli := newClient(cfg)
	_, err := runToolLoop(context.Background(), cli, cfg, []Message{{Role: "user", Content: "keep rolling forever"}})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("want a bounded-rounds error, got %v", err)
	}
}

func TestBuildTools_diceExecutesAndReturnsTotal(t *testing.T) {
	_, exec := BuildTools(nil, ToolsOptions{})
	out, err := exec(context.Background(), ToolCall{Name: "dice", Arguments: json.RawMessage(`{"expression":"2d6+3","seed":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Rolls []struct {
			Total int `json:"total"`
		} `json:"rolls"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(result.Rolls) != 1 {
		t.Fatalf("rolls: %s", out)
	}
}

func TestBuildTools_getReturnsEntityAndAmbiguityError(t *testing.T) {
	st, _, _ := harness(t)
	defer st.Close()
	_, exec := BuildTools(st, ToolsOptions{Edition: edition.All})

	out, err := exec(context.Background(), ToolCall{Name: "get", Arguments: json.RawMessage(`{"kind":"item","name":"Longsword"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "slashing damage") {
		t.Fatalf("expected the entity text in the result: %s", out)
	}

	_, err = exec(context.Background(), ToolCall{Name: "get", Arguments: json.RawMessage(`{"kind":"spell","name":"Fireball"}`)})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected an ambiguity error, got %v", err)
	}
}

func TestBuildTools_listReturnsKindsWithNoKindGiven(t *testing.T) {
	st, _, _ := harness(t)
	defer st.Close()
	_, exec := BuildTools(st, ToolsOptions{Edition: edition.All})

	out, err := exec(context.Background(), ToolCall{Name: "list", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Kinds []string `json:"kinds"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !strings.Contains(strings.Join(result.Kinds, ","), "item") {
		t.Fatalf("expected item among kinds: %s", out)
	}
}

func TestBuildTools_listFiltersNamesByKindAndQuery(t *testing.T) {
	st, _, _ := harness(t)
	defer st.Close()
	_, exec := BuildTools(st, ToolsOptions{Edition: edition.All})

	out, err := exec(context.Background(), ToolCall{Name: "list", Arguments: json.RawMessage(`{"kind":"spell","query":"fire"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Total int `json:"total"`
		Names []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"names"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if result.Total != 2 {
		t.Fatalf("expected both Fireball sources under edition.All, got %d: %s", result.Total, out)
	}

	out, err = exec(context.Background(), ToolCall{Name: "list", Arguments: json.RawMessage(`{"kind":"spell","query":"nonexistent"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if result.Total != 0 {
		t.Fatalf("expected no matches: %s", out)
	}
}

func TestBuildTools_unknownToolErrors(t *testing.T) {
	_, exec := BuildTools(nil, ToolsOptions{})
	if _, err := exec(context.Background(), ToolCall{Name: "nope"}); err == nil {
		t.Fatal("expected an error for an unknown tool")
	}
}
