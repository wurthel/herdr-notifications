package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "123456:SECRET-token"

type recorded struct {
	Method string
	Path   string
	CType  string
	Body   map[string]any
}

type fakeAPI struct {
	mu       sync.Mutex
	requests []recorded
	replies  []func(w http.ResponseWriter)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	i := len(f.requests)
	f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.Header.Get("Content-Type"), body})
	var reply func(http.ResponseWriter)
	if i < len(f.replies) {
		reply = f.replies[i]
	}
	f.mu.Unlock()
	if reply == nil {
		reply = okReply
	}
	reply(w)
}

func (f *fakeAPI) reqs() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

func okReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
}

func errReply(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func newTestClient(t *testing.T, replies ...func(http.ResponseWriter)) (*Client, *fakeAPI, *[]time.Duration) {
	t.Helper()
	api := &fakeAPI{replies: replies}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	c := New(srv.URL+"/", testToken)
	var sleeps []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	return c, api, &sleeps
}

func TestSendHTMLSuccess(t *testing.T) {
	c, api, sleeps := newTestClient(t)
	if err := c.SendHTML(context.Background(), "42", "<b>hi</b>", "hi"); err != nil {
		t.Fatalf("SendHTML: %v", err)
	}
	if len(api.reqs()) != 1 {
		t.Fatalf("requests = %d, want 1", len(api.reqs()))
	}
	r := api.reqs()[0]
	if r.Method != http.MethodPost {
		t.Errorf("method = %s", r.Method)
	}
	if r.Path != "/bot"+testToken+"/sendMessage" {
		t.Errorf("path = %s", r.Path)
	}
	if r.CType != "application/json" {
		t.Errorf("content-type = %s", r.CType)
	}
	want := map[string]any{"chat_id": "42", "text": "<b>hi</b>", "parse_mode": "HTML", "disable_web_page_preview": true}
	if len(r.Body) != len(want) {
		t.Errorf("body = %v, want %v", r.Body, want)
	}
	for k, v := range want {
		if r.Body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, r.Body[k], v)
		}
	}
	if len(*sleeps) != 0 {
		t.Errorf("unexpected sleeps %v", *sleeps)
	}
}

func TestSendHTMLRateLimit(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		maxWait    time.Duration
		second     func(http.ResponseWriter)
		wantSleep  time.Duration
		wantErr    bool
	}{
		{name: "short wait", retryAfter: `"parameters":{"retry_after":2}`, maxWait: 5 * time.Second, wantSleep: 2 * time.Second},
		{name: "capped by max", retryAfter: `"parameters":{"retry_after":60}`, maxWait: 3 * time.Second, wantSleep: 3 * time.Second},
		{name: "missing retry_after uses max", maxWait: 4 * time.Second, wantSleep: 4 * time.Second},
		{
			name: "second 429 is returned", retryAfter: `"parameters":{"retry_after":1}`, maxWait: 5 * time.Second,
			second:    errReply(429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`),
			wantSleep: time.Second, wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"ok":false,"error_code":429,"description":"Too Many Requests"`
			if tt.retryAfter != "" {
				body += "," + tt.retryAfter
			}
			body += "}"
			c, api, sleeps := newTestClient(t, errReply(429, body), tt.second)
			c.MaxRetryWait = tt.maxWait

			err := c.SendHTML(context.Background(), "42", "<b>hi</b>", "hi")
			if len(api.reqs()) != 2 {
				t.Fatalf("requests = %d, want 2", len(api.reqs()))
			}
			if len(*sleeps) != 1 || (*sleeps)[0] != tt.wantSleep {
				t.Errorf("sleeps = %v, want [%v]", *sleeps, tt.wantSleep)
			}
			if api.reqs()[1].Body["parse_mode"] != "HTML" {
				t.Errorf("retry lost parse_mode: %v", api.reqs()[1].Body)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("SendHTML: %v", err)
				}
				return
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != 429 || apiErr.RetryAfter != time.Second {
				t.Errorf("error = %#v, want *APIError 429", err)
			}
		})
	}
}

func TestSendHTMLSleepError(t *testing.T) {
	c, api, _ := newTestClient(t, errReply(429, `{"ok":false,"error_code":429,"description":"x","parameters":{"retry_after":1}}`))
	c.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	err := c.SendHTML(context.Background(), "42", "a", "a")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if len(api.reqs()) != 1 {
		t.Errorf("requests = %d, want 1", len(api.reqs()))
	}
}

func TestSendHTMLParseEntitiesFallback(t *testing.T) {
	c, api, _ := newTestClient(t,
		errReply(400, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: Unsupported start tag \"foo\" at byte offset 0"}`))
	if err := c.SendHTML(context.Background(), "42", "<foo>x", "plain x"); err != nil {
		t.Fatalf("SendHTML: %v", err)
	}
	if len(api.reqs()) != 2 {
		t.Fatalf("requests = %d, want 2", len(api.reqs()))
	}
	second := api.reqs()[1].Body
	if _, ok := second["parse_mode"]; ok {
		t.Errorf("fallback still has parse_mode: %v", second)
	}
	if second["text"] != "plain x" || second["chat_id"] != "42" || second["disable_web_page_preview"] != true {
		t.Errorf("fallback body = %v", second)
	}
}

func TestSendHTMLAPIErrors(t *testing.T) {
	tests := []struct {
		name     string
		reply    func(http.ResponseWriter)
		wantCode int
		wantDesc string
	}{
		{"chat not found", errReply(400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`), 400, "Bad Request: chat not found"},
		{"unauthorized", errReply(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`), 401, "Unauthorized"},
		{"code from http status", errReply(403, `{"ok":false,"description":"Forbidden"}`), 403, "Forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api, _ := newTestClient(t, tt.reply)
			err := c.SendHTML(context.Background(), "42", "x", "x")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want *APIError", err)
			}
			if apiErr.Code != tt.wantCode || apiErr.Description != tt.wantDesc {
				t.Errorf("APIError = %+v", apiErr)
			}
			if len(api.reqs()) != 1 {
				t.Errorf("requests = %d, want 1", len(api.reqs()))
			}
		})
	}
}

func TestSendHTMLUnexpectedResponse(t *testing.T) {
	c, _, _ := newTestClient(t, errReply(502, "<html>bad gateway</html>"))
	err := c.SendHTML(context.Background(), "42", "x", "x")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("error = %v, want unexpected response with HTTP 502", err)
	}
}

func TestSendHTMLTransportErrorRedactsToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	closedURL := srv.URL
	srv.Close()

	tests := []struct {
		name string
		base string
	}{
		{"closed server", closedURL},
		{"invalid url", "http://bad host:%zz/" + testToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(tt.base, testToken)
			err := c.SendHTML(context.Background(), "42", "x", "x")
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error leaks token: %v", err)
			}
			if !strings.Contains(err.Error(), "telegram request failed") {
				t.Errorf("error = %v", err)
			}
		})
	}
}
