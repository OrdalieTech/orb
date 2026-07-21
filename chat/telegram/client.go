// Package telegram implements the Telegram Bot API adapter for the pi-go
// chat gateway: ingress (webhook and long poll), delivery (typing indicator,
// streamed preview edits, HTML finalization with chunking), and media
// download. It plugs into the chat processor via [chat.Adapter].
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the production Bot API endpoint.
const DefaultBaseURL = "https://api.telegram.org"

// notModifiedMarker identifies the harmless edit-with-identical-content
// error, which is treated as success.
const notModifiedMarker = "message is not modified"

// maxCallAttempts bounds flood-control (429) retries per API call.
const maxCallAttempts = 5

// APIError is a decoded Bot API failure envelope.
type APIError struct {
	Method      string
	Code        int
	Description string
	// RetryAfter is the server-requested flood-control pause, when present.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %s: %s (code %d)", e.Method, e.Description, e.Code)
}

// client is the minimal Bot API subset the adapter needs. Every call decodes
// the ok/description/parameters envelope, honors 429 retry_after, and treats
// "message is not modified" as success.
type client struct {
	baseURL string
	token   string
	// http serves regular calls; pollHTTP serves getUpdates with its longer
	// timeout (poll timeout + 10s).
	http     *http.Client
	pollHTTP *http.Client
	// sleep is the flood-control pause; a seam for tests.
	sleep func(ctx context.Context, d time.Duration) error
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *apiParameters  `json:"parameters"`
}

type apiParameters struct {
	RetryAfter int `json:"retry_after"`
}

type apiUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type apiChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type apiEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type apiPhotoSize struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
}

// apiFile covers document, audio, video, and voice payloads; absent fields
// stay zero.
type apiFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type apiMessage struct {
	MessageID       int64          `json:"message_id"`
	Date            int64          `json:"date"`
	ThreadID        int64          `json:"message_thread_id"`
	IsTopicMessage  bool           `json:"is_topic_message"`
	From            *apiUser       `json:"from"`
	Chat            apiChat        `json:"chat"`
	Text            string         `json:"text"`
	Caption         string         `json:"caption"`
	Entities        []apiEntity    `json:"entities"`
	CaptionEntities []apiEntity    `json:"caption_entities"`
	Photo           []apiPhotoSize `json:"photo"`
	Document        *apiFile       `json:"document"`
	Audio           *apiFile       `json:"audio"`
	Video           *apiFile       `json:"video"`
	Voice           *apiFile       `json:"voice"`
	MediaGroupID    string         `json:"media_group_id"`
	ReplyTo         *apiMessage    `json:"reply_to_message"`
}

type apiUpdate struct {
	UpdateID int64       `json:"update_id"`
	Message  *apiMessage `json:"message"`
}

type apiFilePath struct {
	FilePath string `json:"file_path"`
}

type linkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

type replyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}

type sendMessageParams struct {
	ChatID             int64               `json:"chat_id"`
	Text               string              `json:"text"`
	ParseMode          string              `json:"parse_mode,omitempty"`
	MessageThreadID    int64               `json:"message_thread_id,omitempty"`
	LinkPreviewOptions *linkPreviewOptions `json:"link_preview_options,omitempty"`
	ReplyParameters    *replyParameters    `json:"reply_parameters,omitempty"`
}

type editMessageParams struct {
	ChatID             int64               `json:"chat_id"`
	MessageID          int64               `json:"message_id"`
	Text               string              `json:"text"`
	ParseMode          string              `json:"parse_mode,omitempty"`
	LinkPreviewOptions *linkPreviewOptions `json:"link_preview_options,omitempty"`
}

// call posts params to method and decodes the result into out (which may be
// nil). 429 responses are retried after the server-requested pause, bounded
// by maxCallAttempts.
func (c *client) call(ctx context.Context, httpClient *http.Client, method string, params any, out any) error {
	payload, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram: %s: encode params: %w", method, err)
	}
	for attempt := 1; ; attempt++ {
		err := c.doCall(ctx, httpClient, method, payload, out)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 && attempt < maxCallAttempts {
			if sleepErr := c.sleep(ctx, apiErr.RetryAfter); sleepErr != nil {
				return sleepErr
			}
			continue
		}
		return err
	}
}

func (c *client) doCall(ctx context.Context, httpClient *http.Client, method string, payload []byte, out any) error {
	url := c.baseURL + "/bot" + c.token + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram: %s: build request: %w", method, c.redact(err))
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, c.redact(err))
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("telegram: %s: read response: %w", method, c.redact(err))
	}
	var envelope apiResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("telegram: %s: decode response (http %d): %w", method, response.StatusCode, err)
	}
	if !envelope.OK {
		if strings.Contains(envelope.Description, notModifiedMarker) {
			return nil
		}
		apiErr := &APIError{Method: method, Code: envelope.ErrorCode, Description: envelope.Description}
		if envelope.Parameters != nil && envelope.Parameters.RetryAfter > 0 {
			apiErr.RetryAfter = time.Duration(envelope.Parameters.RetryAfter) * time.Second
		}
		return apiErr
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram: %s: decode result: %w", method, err)
		}
	}
	return nil
}

// redact strips the bot token from transport errors (whose URLs embed it) so
// it can never reach logs.
func (c *client) redact(err error) error {
	if err == nil || !strings.Contains(err.Error(), c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "<token>"))
}

func (c *client) getMe(ctx context.Context) (*apiUser, error) {
	var user apiUser
	if err := c.call(ctx, c.http, "getMe", struct{}{}, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (c *client) deleteWebhook(ctx context.Context) error {
	return c.call(ctx, c.http, "deleteWebhook", struct{}{}, nil)
}

func (c *client) getUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]apiUpdate, error) {
	params := struct {
		Offset         int64    `json:"offset"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{Offset: offset, Timeout: timeoutSeconds, AllowedUpdates: []string{"message"}}
	var updates []apiUpdate
	if err := c.call(ctx, c.pollHTTP, "getUpdates", params, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *client) sendMessage(ctx context.Context, params sendMessageParams) (*apiMessage, error) {
	var message apiMessage
	if err := c.call(ctx, c.http, "sendMessage", params, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

func (c *client) editMessageText(ctx context.Context, params editMessageParams) error {
	return c.call(ctx, c.http, "editMessageText", params, nil)
}

func (c *client) sendChatAction(ctx context.Context, chatID, threadID int64, action string) error {
	params := struct {
		ChatID          int64  `json:"chat_id"`
		Action          string `json:"action"`
		MessageThreadID int64  `json:"message_thread_id,omitempty"`
	}{ChatID: chatID, Action: action, MessageThreadID: threadID}
	return c.call(ctx, c.http, "sendChatAction", params, nil)
}

func (c *client) getFile(ctx context.Context, fileID string) (*apiFilePath, error) {
	params := struct {
		FileID string `json:"file_id"`
	}{FileID: fileID}
	var file apiFilePath
	if err := c.call(ctx, c.http, "getFile", params, &file); err != nil {
		return nil, err
	}
	return &file, nil
}

// downloadFile streams the file behind a getFile path. The URL embeds the
// token; errors are redacted so the token is never logged.
func (c *client) downloadFile(ctx context.Context, filePath string) (io.ReadCloser, error) {
	url := c.baseURL + "/file/bot" + c.token + "/" + filePath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: download file: %w", c.redact(err))
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("telegram: download file: %w", c.redact(err))
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("telegram: download file %q: http %d", filePath, response.StatusCode)
	}
	return response.Body, nil
}

// sleepContext pauses for d, honoring ctx cancellation.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
