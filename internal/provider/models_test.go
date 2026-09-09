package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchModels_parsesAndSortsIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Fatalf("want a request to /models, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Fatalf("want the API key sent as a bearer token, got %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gpt-4o-mini"},
				{"id": "text-embedding-3-small"},
			},
		})
	}))
	defer srv.Close()

	models, err := FetchModels(context.Background(), srv.Client(), OpenAI, Credential{APIKey: "sk-test", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-4o-mini", "text-embedding-3-small"}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Fatalf("want %v, got %v", want, models)
	}
}

func TestFetchModels_errorsWithoutAnAPIKey(t *testing.T) {
	if _, err := FetchModels(context.Background(), nil, OpenAI, Credential{}); err == nil {
		t.Fatal("want an error when no API key is configured")
	}
}

func TestFetchModels_surfacesAnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := FetchModels(context.Background(), srv.Client(), OpenAI, Credential{APIKey: "bad", BaseURL: srv.URL})
	if err == nil {
		t.Fatal("want an error on a non-200 response")
	}
}
