package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/OrdalieTech/pigo/ai"
)

const defaultAzureOpenAIAPIVersion = "v1"

type azureOpenAIHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var azureOpenAIHTTPClient azureOpenAIHTTPDoer = http.DefaultClient

type AzureOpenAIResponsesOptions struct {
	ai.StreamOptions
	ReasoningEffort     *string `json:"reasoningEffort,omitempty"`
	ReasoningSummary    *string `json:"reasoningSummary,omitempty"`
	AzureAPIVersion     *string `json:"azureApiVersion,omitempty"`
	AzureResourceName   *string `json:"azureResourceName,omitempty"`
	AzureBaseURL        *string `json:"azureBaseUrl,omitempty"`
	AzureDeploymentName *string `json:"azureDeploymentName,omitempty"`
}

func StreamAzureOpenAIResponses(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: Azure OpenAI Responses model is nil")
	}
	options := &AzureOpenAIResponsesOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamAzureOpenAIResponsesWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleAzureOpenAIResponses(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Azure OpenAI Responses model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	if err := assertAzureOpenAIAPIKey(model, &base); err != nil {
		return nil, err
	}
	var requested *ai.ThinkingLevel
	if options != nil {
		requested = options.Reasoning
	}
	level := clampSimpleReasoning(model, requested)
	var effort *string
	if level != nil {
		value := string(*level)
		effort = &value
	}
	return StreamAzureOpenAIResponsesWithOptions(ctx, model, requestContext, &AzureOpenAIResponsesOptions{
		StreamOptions: base, ReasoningEffort: effort,
	})
}

func StreamAzureOpenAIResponsesWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *AzureOpenAIResponsesOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Azure OpenAI Responses model is nil")
	}
	output := newAssistantMessage(model)
	streamOptions := azureOpenAIStreamOptions(options)
	deploymentName := resolveAzureOpenAIDeploymentName(model, options)

	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		sink := func(event ai.AssistantMessageEvent) bool { return yield(event, nil) }
		fail := func(err error) {
			clearResponsesStreamingFields(output)
			sink(streamFailure(ctx, output, err, "Azure OpenAI API error"))
		}

		apiKey, err := azureOpenAIAPIKey(model, streamOptions)
		if err != nil {
			fail(err)
			return
		}
		config, err := resolveAzureOpenAIConfig(model, options)
		if err != nil {
			fail(err)
			return
		}
		payload, err := buildAzureOpenAIResponsesPayload(model, requestContext, options, deploymentName)
		if err != nil {
			fail(err)
			return
		}
		rawCompat, err := decodeCompat[ai.OpenAIResponsesCompat](model)
		if err != nil {
			fail(err)
			return
		}
		supportsGrammar := rawCompat.SupportsOpenAIGrammarTools != nil && *rawCompat.SupportsOpenAIGrammarTools
		grammarToolInputProperties, err := createGrammarToolInputProperties(requestContext.Tools, supportsGrammar)
		if err != nil {
			fail(err)
			return
		}
		hookedPayload, err := applyPayloadHook(ctx, model, streamOptions, payload)
		if err != nil {
			fail(err)
			return
		}
		response, err := postAzureOpenAIStream(ctx, model, streamOptions, config, apiKey, hookedPayload)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if streamOptions != nil && streamOptions.OnResponse != nil {
			if err := streamOptions.OnResponse(ctx, providerResponse(response), model); err != nil {
				fail(err)
				return
			}
		}
		if !sink(ai.StartEvent{Partial: output}) {
			return
		}

		processor := newOpenAIResponsesProcessor(model, output, nil, sink)
		// Upstream's Azure stream never passes applyServiceTierPricing to
		// processResponsesStream, so service-tier multipliers are ignored (OA-M2).
		processor.applyServiceTierPricing = false
		processor.grammarToolInputProperties = grammarToolInputProperties
		err = readSSE(response.Body, processor.handle)
		if errors.Is(err, errStopSSE) {
			return
		}
		if err == nil && !processor.sawTerminalResponseEvent {
			err = errors.New("OpenAI Responses stream ended before a terminal response event")
		}
		if err == nil && ctx.Err() != nil {
			err = errors.New("Request was aborted") //nolint:staticcheck // Exact upstream error text is observable.
		}
		if err == nil && (output.StopReason == ai.StopReasonAborted || output.StopReason == ai.StopReasonError) {
			err = errors.New("An unknown error occurred") //nolint:staticcheck // Exact upstream error text is observable.
		}
		if err != nil {
			fail(err)
			return
		}
		clearResponsesStreamingFields(output)
		sink(ai.DoneEvent{Reason: output.StopReason, Message: output})
	}, nil
}

func azureOpenAIStreamOptions(options *AzureOpenAIResponsesOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func assertAzureOpenAIAPIKey(model *ai.Model, options *ai.StreamOptions) error {
	_, err := azureOpenAIAPIKey(model, options)
	return err
}

func azureOpenAIAPIKey(model *ai.Model, options *ai.StreamOptions) (string, error) {
	if options != nil && options.APIKey != nil && *options.APIKey != "" {
		return *options.APIKey, nil
	}
	return "", fmt.Errorf("No API key for provider: %s", model.Provider) //nolint:staticcheck // Exact upstream error text is observable.
}

type azureOpenAIConfig struct {
	baseURL    string
	apiVersion string
}

func resolveAzureOpenAIConfig(model *ai.Model, options *AzureOpenAIResponsesOptions) (azureOpenAIConfig, error) {
	streamOptions := azureOpenAIStreamOptions(options)
	apiVersion := ""
	if options != nil && options.AzureAPIVersion != nil {
		apiVersion = *options.AzureAPIVersion
	}
	if apiVersion == "" {
		apiVersion = providerEnvValue("AZURE_OPENAI_API_VERSION", streamOptions)
	}
	if apiVersion == "" {
		apiVersion = defaultAzureOpenAIAPIVersion
	}

	baseURL := ""
	resourceName := ""
	if options != nil && options.AzureBaseURL != nil {
		baseURL = strings.TrimSpace(*options.AzureBaseURL)
	}
	if options != nil && options.AzureResourceName != nil {
		resourceName = *options.AzureResourceName
	}
	if baseURL == "" {
		baseURL = strings.TrimSpace(providerEnvValue("AZURE_OPENAI_BASE_URL", streamOptions))
	}
	if resourceName == "" {
		resourceName = providerEnvValue("AZURE_OPENAI_RESOURCE_NAME", streamOptions)
	}
	if baseURL == "" && resourceName != "" {
		baseURL = buildAzureOpenAIDefaultBaseURL(resourceName)
	}
	if baseURL == "" {
		baseURL = model.BaseURL
	}
	if baseURL == "" {
		return azureOpenAIConfig{}, errors.New("Azure OpenAI base URL is required. Set AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME, or pass azureBaseUrl, azureResourceName, or model.baseUrl.") //nolint:staticcheck // Exact upstream error text is observable.
	}
	normalized, err := normalizeAzureOpenAIBaseURL(baseURL)
	if err != nil {
		return azureOpenAIConfig{}, err
	}
	return azureOpenAIConfig{baseURL: normalized, apiVersion: apiVersion}, nil
}

func normalizeAzureOpenAIBaseURL(baseURL string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("Invalid Azure OpenAI base URL: %s", baseURL) //nolint:staticcheck // Exact upstream error text is observable.
	}
	host := strings.ToLower(parsed.Hostname())
	isAzureHost := strings.HasSuffix(host, ".openai.azure.com") ||
		strings.HasSuffix(host, ".cognitiveservices.azure.com") ||
		strings.HasSuffix(host, ".ai.azure.com")
	normalizedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	if isAzureHost && (normalizedPath == "" || normalizedPath == "/" || normalizedPath == "/openai" || normalizedPath == "/openai/v1/responses") {
		parsed.Path = "/openai/v1"
		parsed.RawPath = ""
		parsed.RawQuery = ""
		parsed.ForceQuery = false
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func buildAzureOpenAIDefaultBaseURL(resourceName string) string {
	return "https://" + resourceName + ".openai.azure.com/openai/v1"
}

func parseAzureOpenAIDeploymentNameMap(value string) map[string]string {
	result := make(map[string]string)
	for _, entry := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return result
}

func resolveAzureOpenAIDeploymentName(model *ai.Model, options *AzureOpenAIResponsesOptions) string {
	if options != nil && options.AzureDeploymentName != nil && *options.AzureDeploymentName != "" {
		return *options.AzureDeploymentName
	}
	mapping := parseAzureOpenAIDeploymentNameMap(providerEnvValue("AZURE_OPENAI_DEPLOYMENT_NAME_MAP", azureOpenAIStreamOptions(options)))
	if deployment := mapping[model.ID]; deployment != "" {
		return deployment
	}
	return model.ID
}

func resolveAzureDeploymentName(model *ai.Model, options *AzureOpenAIResponsesOptions) string {
	return resolveAzureOpenAIDeploymentName(model, options)
}

func buildAzureOpenAIResponsesPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *AzureOpenAIResponsesOptions,
	deploymentName string,
) (*OpenAIResponsesPayload, error) {
	compat, err := getOpenAIResponsesCompat(model)
	if err != nil {
		return nil, err
	}
	rawCompat, err := decodeCompat[ai.OpenAIResponsesCompat](model)
	if err != nil {
		return nil, err
	}
	supportsStrictMode := true
	if rawCompat.SupportsStrictMode != nil {
		supportsStrictMode = *rawCompat.SupportsStrictMode
	}
	supportsGrammar := rawCompat.SupportsOpenAIGrammarTools != nil && *rawCompat.SupportsOpenAIGrammarTools
	grammarToolInputProperties, err := createGrammarToolInputProperties(requestContext.Tools, supportsGrammar)
	if err != nil {
		return nil, err
	}
	toolOptions := responsesToolOptions{
		supportsStrictMode: supportsStrictMode, supportsOpenAIGrammarTools: supportsGrammar,
	}
	input, err := convertResponsesMessagesWithOptions(model, requestContext, map[string]ai.Tool{}, responsesMessageOptions{
		supportsDeveloperRole:      compat.supportsDeveloperRole,
		grammarToolInputProperties: grammarToolInputProperties,
		toolOptions:                toolOptions,
	})
	if err != nil {
		return nil, err
	}
	payload := &OpenAIResponsesPayload{Model: deploymentName, Input: input, Stream: true, Store: false}
	streamOptions := azureOpenAIStreamOptions(options)
	if streamOptions != nil {
		if streamOptions.SessionID != nil {
			if value, ok := clampOpenAIPromptCacheKey(streamOptions.SessionID).(string); ok {
				payload.PromptCacheKey = &value
			}
		}
		if streamOptions.MaxTokens != nil && *streamOptions.MaxTokens != 0 {
			value := max(*streamOptions.MaxTokens, openAIResponsesMinOutputTokens)
			payload.MaxOutputTokens = &value
		}
		payload.Temperature = streamOptions.Temperature
	}
	if requestContext.Tools != nil && len(*requestContext.Tools) > 0 {
		payload.Tools, err = convertResponsesToolsWithOptions(*requestContext.Tools, toolOptions)
		if err != nil {
			return nil, err
		}
	}
	applyAzureOpenAIReasoning(payload, model, options)
	return payload, nil
}

func applyAzureOpenAIReasoning(payload *OpenAIResponsesPayload, model *ai.Model, options *AzureOpenAIResponsesOptions) {
	if !model.Reasoning {
		return
	}
	effortSet := options != nil && options.ReasoningEffort != nil && *options.ReasoningEffort != ""
	summarySet := options != nil && options.ReasoningSummary != nil && *options.ReasoningSummary != ""
	if effortSet || summarySet {
		effort := "medium"
		if effortSet {
			effort = mappedThinkingLevel(model, *options.ReasoningEffort, *options.ReasoningEffort)
		}
		summary := "auto"
		if summarySet {
			summary = *options.ReasoningSummary
		}
		payload.Reasoning = &OpenAIReasoningParams{Effort: effort, Summary: &summary}
		payload.Include = []string{"reasoning.encrypted_content"}
	} else if supportsOffReasoning(model) {
		payload.Reasoning = &OpenAIReasoningParams{Effort: mappedThinkingLevel(model, "off", "none")}
	}
}

func postAzureOpenAIStream(
	ctx context.Context,
	model *ai.Model,
	options *ai.StreamOptions,
	config azureOpenAIConfig,
	apiKey string,
	payload any,
) (*http.Response, error) {
	body, err := ai.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Azure OpenAI request: %w", err)
	}
	endpoint, err := url.Parse(strings.TrimRight(config.baseURL, "/") + "/responses")
	if err != nil {
		return nil, err
	}
	// The pinned TypeScript SDK replaces the base URL's query when it applies
	// api-version. If the configured proxy URL already has a query, /responses
	// was parsed into that query and is discarded with it.
	endpoint.RawQuery = url.Values{"api-version": []string{config.apiVersion}}.Encode()
	headers := copyModelHeaders(model)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	headers.Set("api-key", apiKey)
	if options != nil {
		mergeProviderHeaders(headers, options.Headers)
	}
	headers, err = applyHeadersHook(ctx, model, options, headers)
	if err != nil {
		return nil, err
	}
	httpClient, err := openAIHeaderTimeoutClient(azureOpenAIHTTPClient, streamTimeoutMS(options), headers)
	if err != nil {
		return nil, err
	}
	// Upstream 7af8533c: the bespoke retry loop is replaced by the shared
	// wrapper, which owns classification, backoff and the abort signal.
	var lastResponse *http.Response
	response, err := retryProviderRequest(ctx, options, func() (*http.Response, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for name, values := range headers {
			request.Header[name] = append([]string(nil), values...)
		}
		attempt, requestErr := httpClient.Do(request)
		if lastResponse != nil && lastResponse != attempt && lastResponse.Body != nil {
			_ = lastResponse.Body.Close()
		}
		lastResponse = attempt
		if requestErr != nil {
			return attempt, requestErr
		}
		if attempt == nil {
			return nil, errors.New("ai/api: Azure OpenAI API returned no HTTP response")
		}
		if attempt.StatusCode >= http.StatusBadRequest {
			contents, readErr := io.ReadAll(attempt.Body)
			_ = attempt.Body.Close()
			if readErr != nil {
				return attempt, readErr
			}
			return attempt, &retryableHTTPStatusError{
				status:  attempt.StatusCode,
				headers: attempt.Header,
				inner:   newOpenAIStatusError(attempt.StatusCode, contents),
			}
		}
		return attempt, nil
	})
	if err != nil {
		var statusError *retryableHTTPStatusError
		if errors.As(err, &statusError) {
			return response, statusError.inner
		}
		return response, err
	}
	return response, nil
}
