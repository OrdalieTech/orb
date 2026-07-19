package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
)

func openRouterImagesTestModel() *ai.ImagesModel {
	headers := map[string]string{"HTTP-Referer": "https://example.com"}
	return &ai.ImagesModel{
		ID:       "google/gemini-3.1-flash-image-preview",
		Name:     "Gemini 3.1 Flash Image Preview",
		API:      ai.ImagesAPIOpenRouter,
		Provider: ai.ImagesProviderOpenRouter,
		BaseURL:  "https://fixture.invalid/api/v1",
		Input:    ai.InputModalities{ai.InputText, ai.InputImage},
		Output:   ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:     ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.015, Output: 0.03}},
		Headers:  &headers,
	}
}

const openRouterImagesSuccessBody = `{"id":"img-1","usage":{"prompt_tokens":12,"completion_tokens":34,` +
	`"prompt_tokens_details":{"cached_tokens":0}},"choices":[{"message":{"content":"Here is your image.",` +
	`"images":[{"image_url":"data:image/png;base64,ZmFrZS1wbmc="}]}}]}`

func openRouterImagesTransport(t *testing.T, status int, body string, requests *[]*http.Request, bodies *[]string) func() {
	t.Helper()
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		if requests != nil {
			*requests = append(*requests, request)
		}
		if bodies != nil {
			contents, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			*bodies = append(*bodies, string(contents))
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Fixture": []string{"yes"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	return func() { openAIHTTPClient = previousClient }
}

func TestOpenRouterImagesReturnsTextPlusImages(t *testing.T) {
	var requests []*http.Request
	var bodies []string
	restore := openRouterImagesTransport(t, http.StatusOK, openRouterImagesSuccessBody, &requests, &bodies)
	defer restore()

	key := "test"
	output, err := GenerateOpenRouterImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, &ai.ImagesOptions{APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}

	if len(requests) != 1 {
		t.Fatalf("request count = %d", len(requests))
	}
	request := requests[0]
	if got := request.URL.String(); got != "https://fixture.invalid/api/v1/chat/completions" {
		t.Fatalf("request URL = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test" {
		t.Fatalf("authorization header = %q", got)
	}
	if got := request.Header.Get("HTTP-Referer"); got != "https://example.com" {
		t.Fatalf("model header = %q", got)
	}
	if got := request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	wantBody := `{"model":"google/gemini-3.1-flash-image-preview","messages":[{"role":"user",` +
		`"content":[{"type":"text","text":"Generate a dog"}]}],"stream":false,"modalities":["image","text"]}`
	if bodies[0] != wantBody {
		t.Fatalf("request body = %s\nwant %s", bodies[0], wantBody)
	}

	if output.StopReason != ai.ImagesStopReasonStop {
		t.Fatalf("stop reason = %q (error %v)", output.StopReason, output.ErrorMessage)
	}
	if output.ResponseID == nil || *output.ResponseID != "img-1" {
		t.Fatalf("response id = %v", output.ResponseID)
	}
	if output.API != ai.ImagesAPIOpenRouter || output.Provider != ai.ImagesProviderOpenRouter ||
		output.Model != "google/gemini-3.1-flash-image-preview" {
		t.Fatalf("identity = %#v", output)
	}
	if output.Timestamp <= 0 {
		t.Fatalf("timestamp = %d", output.Timestamp)
	}
	if len(output.Output) != 2 {
		t.Fatalf("output length = %d", len(output.Output))
	}
	text, ok := output.Output[0].(*ai.TextContent)
	if !ok || text.Text != "Here is your image." {
		t.Fatalf("output[0] = %#v", output.Output[0])
	}
	image, ok := output.Output[1].(*ai.ImageContent)
	if !ok || image.MimeType != "image/png" || image.Data != "ZmFrZS1wbmc=" {
		t.Fatalf("output[1] = %#v", output.Output[1])
	}
	if output.Usage == nil {
		t.Fatal("usage missing")
	}
	if output.Usage.Input != 12 || output.Usage.Output != 34 || output.Usage.CacheRead != 0 ||
		output.Usage.CacheWrite != 0 || output.Usage.TotalTokens != 46 {
		t.Fatalf("usage = %#v", output.Usage)
	}
	// Rates flow through runtime float64 arithmetic exactly as upstream's JS
	// expression (model.cost.input / 1000000) * input.
	rates := openRouterImagesTestModel().Cost
	wantInputCost := rates.Input / 1_000_000 * 12
	wantOutputCost := rates.Output / 1_000_000 * 34
	if output.Usage.Cost.Input != wantInputCost || output.Usage.Cost.Output != wantOutputCost ||
		output.Usage.Cost.Total != wantInputCost+wantOutputCost {
		t.Fatalf("cost = %#v", output.Usage.Cost)
	}
}

func TestOpenRouterImagesImageOnlyModalitiesAndImageInput(t *testing.T) {
	var bodies []string
	restore := openRouterImagesTransport(t, http.StatusOK, openRouterImagesSuccessBody, nil, &bodies)
	defer restore()

	model := openRouterImagesTestModel()
	model.ID = "black-forest-labs/flux.2-pro"
	model.Output = ai.InputModalities{ai.InputImage}
	key := "test"
	output, err := GenerateOpenRouterImages(context.Background(), model, ai.ImagesContext{
		Input: ai.ImagesContent{
			&ai.TextContent{Text: "Vary this"},
			&ai.ImageContent{MimeType: "image/png", Data: "QUJD"},
		},
	}, &ai.ImagesOptions{APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonStop {
		t.Fatalf("stop reason = %q (error %v)", output.StopReason, output.ErrorMessage)
	}
	wantBody := `{"model":"black-forest-labs/flux.2-pro","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"Vary this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}],` +
		`"stream":false,"modalities":["image"]}`
	if bodies[0] != wantBody {
		t.Fatalf("request body = %s\nwant %s", bodies[0], wantBody)
	}
}

func TestOpenRouterImagesAbortedSignal(t *testing.T) {
	var requests []*http.Request
	restore := openRouterImagesTransport(t, http.StatusOK, openRouterImagesSuccessBody, &requests, nil)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	key := "test"
	output, err := GenerateOpenRouterImages(ctx, openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, &ai.ImagesOptions{APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonAborted {
		t.Fatalf("stop reason = %q", output.StopReason)
	}
	if output.ErrorMessage == nil || *output.ErrorMessage == "" {
		t.Fatalf("error message = %v", output.ErrorMessage)
	}
	if len(requests) != 0 {
		t.Fatalf("aborted request still recorded %d requests", len(requests))
	}
}

func TestGenerateImagesDispatch(t *testing.T) {
	restore := openRouterImagesTransport(t, http.StatusOK, openRouterImagesSuccessBody, nil, nil)
	defer restore()

	if _, err := GenerateImages(context.Background(), nil, ai.ImagesContext{}, nil); err == nil ||
		err.Error() != "ai: images model is nil" {
		t.Fatalf("nil model error = %v", err)
	}

	unknown := openRouterImagesTestModel()
	unknown.API = "faux-images"
	if _, err := GenerateImages(context.Background(), unknown, ai.ImagesContext{}, nil); err == nil ||
		err.Error() != "No API provider registered for api: faux-images" {
		t.Fatalf("unknown api error = %v", err)
	}

	key := "test"
	output, err := GenerateImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, &ai.ImagesOptions{APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	hasImage := false
	for _, block := range output.Output {
		if _, ok := block.(*ai.ImageContent); ok {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("dispatched result missing image: %#v", output.Output)
	}
}

func TestOpenRouterImagesErrorMapping(t *testing.T) {
	var requests []*http.Request
	restore := openRouterImagesTransport(t, http.StatusTooManyRequests,
		`{"error":{"message":"Rate limited","type":"rate_limit"}}`, &requests, nil)
	defer restore()

	key := "test"
	output, err := GenerateOpenRouterImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, &ai.ImagesOptions{APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonError {
		t.Fatalf("stop reason = %q", output.StopReason)
	}
	want := `429: {"message":"Rate limited","type":"rate_limit"}`
	if output.ErrorMessage == nil || *output.ErrorMessage != want {
		t.Fatalf("error message = %v, want %s", output.ErrorMessage, want)
	}
	if len(output.Output) != 0 || output.ResponseID != nil || output.Usage != nil {
		t.Fatalf("failed result carried payload: %#v", output)
	}
	// maxRetries defaults to 0 upstream, so a retryable 429 is not retried.
	if len(requests) != 1 {
		t.Fatalf("request count = %d", len(requests))
	}
}

func TestOpenRouterImagesRequiresAPIKey(t *testing.T) {
	var requests []*http.Request
	restore := openRouterImagesTransport(t, http.StatusOK, openRouterImagesSuccessBody, &requests, nil)
	defer restore()

	output, err := GenerateOpenRouterImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonError {
		t.Fatalf("stop reason = %q", output.StopReason)
	}
	if output.ErrorMessage == nil || *output.ErrorMessage != "No API key for provider: openrouter" {
		t.Fatalf("error message = %v", output.ErrorMessage)
	}
	if len(requests) != 0 {
		t.Fatalf("request count = %d", len(requests))
	}
}

func TestOpenRouterImagesSkipsNonDataImages(t *testing.T) {
	body := `{"id":"img-2","choices":[{"message":{"content":"","images":[` +
		`{"image_url":"https://example.com/dog.png"},` +
		`{"image_url":{"url":"data:image/jpeg;base64,QUJD"}},` +
		`{"image_url":"data:image/png;base64"},` +
		`{"image_url":{"url":"data:;base64,QUJD"}},` +
		`{},` +
		`{"image_url":{"url":"data:image/png;base64,QUJD\nRA=="}}]}}]}`
	restore := openRouterImagesTransport(t, http.StatusOK, body, nil, nil)
	defer restore()

	key := "test"
	output, err := GenerateOpenRouterImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, &ai.ImagesOptions{APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonStop {
		t.Fatalf("stop reason = %q (error %v)", output.StopReason, output.ErrorMessage)
	}
	if len(output.Output) != 1 {
		t.Fatalf("output = %#v", output.Output)
	}
	image, ok := output.Output[0].(*ai.ImageContent)
	if !ok || image.MimeType != "image/jpeg" || image.Data != "QUJD" {
		t.Fatalf("output[0] = %#v", output.Output[0])
	}
	if output.Usage != nil {
		t.Fatalf("usage without provider usage = %#v", output.Usage)
	}
}

func TestOpenRouterImagesUsageCacheAccounting(t *testing.T) {
	model := openRouterImagesTestModel()
	model.Cost = ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 2, Output: 4, CacheRead: 1, CacheWrite: 3}}

	usage := parseOpenRouterImagesUsage([]byte(
		`{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":50,"cache_write_tokens":30}}`,
	), model)
	if usage.Input != 50 || usage.Output != 5 || usage.CacheRead != 20 || usage.CacheWrite != 30 || usage.TotalTokens != 105 {
		t.Fatalf("usage = %#v", usage)
	}
	if usage.Reasoning != nil {
		t.Fatalf("images usage carried reasoning tokens: %#v", usage.Reasoning)
	}
	wantCost := ai.Cost{
		Input:      model.Cost.Input / 1_000_000 * 50,
		Output:     model.Cost.Output / 1_000_000 * 5,
		CacheRead:  model.Cost.CacheRead / 1_000_000 * 20,
		CacheWrite: model.Cost.CacheWrite / 1_000_000 * 30,
	}
	wantCost.Total = wantCost.Input + wantCost.Output + wantCost.CacheRead + wantCost.CacheWrite
	if usage.Cost != wantCost {
		t.Fatalf("cost = %#v, want %#v", usage.Cost, wantCost)
	}

	// Reported cached tokens below the cache write count clamp cacheRead to zero.
	clamped := parseOpenRouterImagesUsage([]byte(
		`{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":10,"cache_write_tokens":30}}`,
	), model)
	if clamped.Input != 70 || clamped.CacheRead != 0 || clamped.CacheWrite != 30 || clamped.TotalTokens != 105 {
		t.Fatalf("clamped usage = %#v", clamped)
	}
}

func TestOpenRouterImagesHooksAndHeaders(t *testing.T) {
	var requests []*http.Request
	var bodies []string
	restore := openRouterImagesTransport(t, http.StatusOK, openRouterImagesSuccessBody, &requests, &bodies)
	defer restore()

	key := "test"
	referer := "https://override.example"
	extra := "extension"
	var hookPayload map[string]any
	var hookResponse *ai.ProviderResponse
	options := &ai.ImagesOptions{
		APIKey: &key,
		Headers: ai.ProviderHeaders{
			"HTTP-Referer": &referer,
			"X-Title":      &extra,
			"X-Removed":    nil,
		},
		OnPayload: func(_ context.Context, payload any, _ *ai.ImagesModel) (any, bool, error) {
			hookPayload, _ = payload.(map[string]any)
			return map[string]any{"model": "replaced", "stream": false, "zz_extra": true}, true, nil
		},
		OnResponse: func(_ context.Context, response ai.ProviderResponse, _ *ai.ImagesModel) error {
			hookResponse = &response
			return nil
		},
	}
	model := openRouterImagesTestModel()
	(*model.Headers)["X-Removed"] = "model-value"
	output, err := GenerateOpenRouterImages(context.Background(), model, ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonStop {
		t.Fatalf("stop reason = %q (error %v)", output.StopReason, output.ErrorMessage)
	}
	if hookPayload == nil || hookPayload["model"] != "google/gemini-3.1-flash-image-preview" {
		t.Fatalf("payload hook input = %#v", hookPayload)
	}
	if bodies[0] != `{"model":"replaced","stream":false,"zz_extra":true}` {
		t.Fatalf("replaced body = %s", bodies[0])
	}
	request := requests[0]
	if got := request.Header.Get("HTTP-Referer"); got != "https://override.example" {
		t.Fatalf("overridden header = %q", got)
	}
	if got := request.Header.Get("X-Title"); got != "extension" {
		t.Fatalf("extra header = %q", got)
	}
	if got := request.Header.Get("X-Removed"); got != "" {
		t.Fatalf("suppressed header = %q", got)
	}
	if hookResponse == nil || hookResponse.Status != http.StatusOK || hookResponse.Headers["x-fixture"] != "yes" {
		t.Fatalf("response hook = %#v", hookResponse)
	}
}

func TestOpenRouterImagesOnResponseErrorBecomesResult(t *testing.T) {
	restore := openRouterImagesTransport(t, http.StatusOK, openRouterImagesSuccessBody, nil, nil)
	defer restore()

	key := "test"
	output, err := GenerateOpenRouterImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, &ai.ImagesOptions{
		APIKey: &key,
		OnResponse: func(context.Context, ai.ProviderResponse, *ai.ImagesModel) error {
			return context.DeadlineExceeded
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonError {
		t.Fatalf("stop reason = %q", output.StopReason)
	}
	if output.ErrorMessage == nil || *output.ErrorMessage != context.DeadlineExceeded.Error() {
		t.Fatalf("error message = %v", output.ErrorMessage)
	}
	if output.ResponseID != nil || len(output.Output) != 0 {
		t.Fatalf("hook failure still parsed response: %#v", output)
	}
}
