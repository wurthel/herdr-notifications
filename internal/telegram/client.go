package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultTimeout      = 10 * time.Second
	DefaultMaxRetryWait = 5 * time.Second
)

type Client struct {
	BaseURL      string
	Token        string
	HTTP         *http.Client
	MaxRetryWait time.Duration
	Sleep        func(context.Context, time.Duration) error
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Token:        token,
		HTTP:         &http.Client{Timeout: DefaultTimeout},
		MaxRetryWait: DefaultMaxRetryWait,
		Sleep:        sleepCtx,
	}
}

type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram api error %d: %s", e.Code, e.Description)
}

type sendRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// SendHTML sends htmlText with parse_mode=HTML. It retries once after a 429
// and, if Telegram cannot parse the markup, resends plainText without it.
func (c *Client) SendHTML(ctx context.Context, chatID, htmlText, plainText string) error {
	req := sendRequest{ChatID: chatID, Text: htmlText, ParseMode: "HTML", DisableWebPagePreview: true}
	err := c.sendWithRetry(ctx, req)

	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(apiErr.Description), "parse entities") {
		req.Text, req.ParseMode = plainText, ""
		return c.sendWithRetry(ctx, req)
	}
	return err
}

func (c *Client) sendWithRetry(ctx context.Context, req sendRequest) error {
	err := c.send(ctx, req)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusTooManyRequests {
		return err
	}
	wait := apiErr.RetryAfter
	if wait <= 0 || wait > c.MaxRetryWait {
		wait = c.MaxRetryWait
	}
	if err := c.Sleep(ctx, wait); err != nil {
		return err
	}
	return c.send(ctx, req)
}

func (c *Client) send(ctx context.Context, req sendRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	endpoint := c.BaseURL + "/bot" + c.Token + "/sendMessage"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return c.redact(err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return c.redact(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return c.redact(err)
	}
	var parsed apiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("telegram: unexpected response (HTTP %d)", resp.StatusCode)
	}
	if !parsed.OK {
		code := parsed.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{
			Code:        code,
			Description: parsed.Description,
			RetryAfter:  time.Duration(parsed.Parameters.RetryAfter) * time.Second,
		}
	}
	return nil
}

// redact drops the request URL (which embeds the bot token) from transport
// errors and scrubs the token from whatever text remains.
func (c *Client) redact(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	msg := err.Error()
	if c.Token != "" {
		msg = strings.ReplaceAll(msg, c.Token, "<redacted>")
	}
	return fmt.Errorf("telegram request failed: %s", msg)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
