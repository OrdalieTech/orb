package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/api"
	"github.com/OrdalieTech/orb/conformance/runner"
	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func bedrockTestModel(id, name string) *ai.Model {
	return &ai.Model{
		ID: id, Name: name, API: ai.APIBedrockConverse, Provider: "amazon-bedrock",
		BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", Reasoning: true,
		Input:         ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
		ContextWindow: 200_000, MaxTokens: 64_000,
	}
}

func helloContext() ai.Context {
	return ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}}}
}

type transportFunc func(context.Context, *api.BedrockConverseStreamPayload) (api.BedrockResponse, error)

func (function transportFunc) Send(ctx context.Context, payload *api.BedrockConverseStreamPayload) (api.BedrockResponse, error) {
	return function(ctx, payload)
}

var errInputCaptured = errors.New("SDK input captured")

// hookedSDKInput runs the ai/api adapter with an onPayload hook and returns
// the SDK input this backend would send for the hooked payload.
func hookedSDKInput(t *testing.T, hook func(context.Context, any, *ai.Model) (any, bool, error)) *bedrockruntime.ConverseStreamInput {
	t.Helper()
	var input *bedrockruntime.ConverseStreamInput
	backend := api.BedrockBackend{NewTransport: func(context.Context, api.BedrockTransportConfig) (api.BedrockTransport, error) {
		return transportFunc(func(_ context.Context, payload *api.BedrockConverseStreamPayload) (api.BedrockResponse, error) {
			converted, err := bedrockSDKInput(payload)
			if err != nil {
				return nil, err
			}
			input = converted
			return nil, errInputCaptured
		}), nil
	}}
	stream, err := api.StreamBedrockConverseWithOptions(context.Background(), bedrockTestModel("anthropic.claude-sonnet-4-5", "Claude"), helloContext(),
		&api.BedrockConverseStreamOptions{StreamOptions: ai.StreamOptions{OnPayload: hook}, Backend: &backend})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if input == nil {
		t.Fatalf("no SDK input was built: %v", message.ErrorMessage)
	}
	return input
}

// TestBedrockPayloadHookPreservesUnmodeledFields_OTM7 pins the upstream hook
// contract (bedrock-converse-stream.ts:223-239): the onPayload return is used
// verbatim as the ConverseStreamCommand input, so hook-injected members the
// typed Go payload does not model (guardrailConfig, performanceConfig, topP,
// stopSequences, ...) must reach the SDK input instead of being silently
// dropped. (OT-M7)
func TestBedrockPayloadHookPreservesUnmodeledFields_OTM7(t *testing.T) {
	input := hookedSDKInput(t, func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
		encoded, err := ai.Marshal(payload)
		if err != nil {
			return nil, false, err
		}
		var generic map[string]any
		if err := json.Unmarshal(encoded, &generic); err != nil {
			return nil, false, err
		}
		generic["guardrailConfig"] = map[string]any{
			"guardrailIdentifier": "guardrail-1",
			"guardrailVersion":    "2",
			"trace":               "enabled",
		}
		generic["performanceConfig"] = map[string]any{"latency": "optimized"}
		generic["serviceTier"] = map[string]any{"type": "priority"}
		generic["outputConfig"] = map[string]any{"textFormat": map[string]any{
			"type": "json_schema",
			"structure": map[string]any{"jsonSchema": map[string]any{
				"name": "answer", "description": "structured answer", "schema": `{"type":"object"}`,
			}},
		}}
		generic["additionalModelResponseFieldPaths"] = []string{"/stop_sequence"}
		generic["promptVariables"] = map[string]any{"topic": map[string]any{"text": "space"}}
		inference, _ := generic["inferenceConfig"].(map[string]any)
		if inference == nil {
			inference = map[string]any{}
		}
		inference["topP"] = 0.9
		inference["stopSequences"] = []string{"STOP"}
		generic["inferenceConfig"] = inference
		return generic, true, nil
	})
	if input.GuardrailConfig == nil ||
		input.GuardrailConfig.GuardrailIdentifier == nil || *input.GuardrailConfig.GuardrailIdentifier != "guardrail-1" ||
		input.GuardrailConfig.GuardrailVersion == nil || *input.GuardrailConfig.GuardrailVersion != "2" ||
		string(input.GuardrailConfig.Trace) != "enabled" {
		t.Fatalf("hook-injected guardrailConfig did not reach the SDK input: %#v", input.GuardrailConfig)
	}
	if input.PerformanceConfig == nil || string(input.PerformanceConfig.Latency) != "optimized" {
		t.Fatalf("hook-injected performanceConfig did not reach the SDK input: %#v", input.PerformanceConfig)
	}
	if input.ServiceTier == nil || string(input.ServiceTier.Type) != "priority" {
		t.Fatalf("hook-injected serviceTier did not reach the SDK input: %#v", input.ServiceTier)
	}
	if input.OutputConfig == nil || input.OutputConfig.TextFormat == nil ||
		string(input.OutputConfig.TextFormat.Type) != "json_schema" {
		t.Fatalf("hook-injected outputConfig did not reach the SDK input: %#v", input.OutputConfig)
	}
	outputSchema, ok := input.OutputConfig.TextFormat.Structure.(*bedrocktypes.OutputFormatStructureMemberJsonSchema)
	if !ok || outputSchema.Value.Schema == nil || *outputSchema.Value.Schema != `{"type":"object"}` ||
		outputSchema.Value.Name == nil || *outputSchema.Value.Name != "answer" ||
		outputSchema.Value.Description == nil || *outputSchema.Value.Description != "structured answer" {
		t.Fatalf("hook-injected output schema = %#v", input.OutputConfig.TextFormat.Structure)
	}
	if len(input.AdditionalModelResponseFieldPaths) != 1 || input.AdditionalModelResponseFieldPaths[0] != "/stop_sequence" {
		t.Fatalf("hook-injected response field paths = %#v", input.AdditionalModelResponseFieldPaths)
	}
	topic, ok := input.PromptVariables["topic"].(*bedrocktypes.PromptVariableValuesMemberText)
	if !ok || topic.Value != "space" {
		t.Fatalf("hook-injected promptVariables = %#v", input.PromptVariables)
	}
	if input.InferenceConfig == nil || input.InferenceConfig.TopP == nil || *input.InferenceConfig.TopP != 0.9 {
		t.Fatalf("hook-injected topP = %#v", input.InferenceConfig)
	}
	if len(input.InferenceConfig.StopSequences) != 1 || input.InferenceConfig.StopSequences[0] != "STOP" {
		t.Fatalf("hook-injected stopSequences = %#v", input.InferenceConfig.StopSequences)
	}
}

func TestBedrockPayloadHookPreservesInferenceConfigDeletion_OTM7(t *testing.T) {
	input := hookedSDKInput(t, func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
		encoded, err := ai.Marshal(payload)
		if err != nil {
			return nil, false, err
		}
		var replacement map[string]any
		if err := json.Unmarshal(encoded, &replacement); err != nil {
			return nil, false, err
		}
		delete(replacement, "inferenceConfig")
		return replacement, true, nil
	})
	if input.InferenceConfig != nil {
		t.Fatalf("deleted inferenceConfig was recreated at the SDK boundary: %#v", input.InferenceConfig)
	}
}

func TestBedrockSDKInputRequiresIntegerMaxTokens(t *testing.T) {
	for _, value := range []float64{3.5, 2_147_483_648} {
		t.Run(fmt.Sprintf("%g", value), func(t *testing.T) {
			_, err := bedrockSDKInput(&api.BedrockConverseStreamPayload{
				ModelID: "fixture", InferenceConfig: api.BedrockInferenceConfig{MaxTokens: &value},
			})
			if err == nil || !strings.Contains(err.Error(), "is not an SDK int32 value") {
				t.Fatalf("maxTokens %g error = %v", value, err)
			}
		})
	}
	valid := float64(777)
	input, err := bedrockSDKInput(&api.BedrockConverseStreamPayload{
		ModelID: "fixture", InferenceConfig: api.BedrockInferenceConfig{MaxTokens: &valid},
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.InferenceConfig == nil || input.InferenceConfig.MaxTokens == nil || *input.InferenceConfig.MaxTokens != 777 {
		t.Fatalf("SDK maxTokens = %#v", input.InferenceConfig)
	}
}

func TestAWSBedrockTransportAuthenticationHeadersAndErrorBody(t *testing.T) {
	for _, name := range []string{"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		t.Setenv(name, "")
	}
	cases := []struct {
		name         string
		options      *api.BedrockConverseStreamOptions
		authContains string
	}{
		{
			name: "skip-auth-dummy-sigv4",
			options: &api.BedrockConverseStreamOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{
				"AWS_BEDROCK_SKIP_AUTH": "1", "AWS_REGION": "us-east-1", "NO_PROXY": "*",
			}}},
			authContains: "Credential=dummy-access-key/",
		},
		{
			name: "static-sigv4",
			options: &api.BedrockConverseStreamOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{
				"AWS_ACCESS_KEY_ID": "fixture-access", "AWS_SECRET_ACCESS_KEY": "fixture-secret", "AWS_REGION": "us-east-1", "NO_PROXY": "*",
			}}},
			authContains: "Credential=fixture-access/",
		},
		{
			name:         "bearer",
			options:      &api.BedrockConverseStreamOptions{Region: "us-east-1", BearerToken: "fixture-bearer", StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{"NO_PROXY": "*"}}},
			authContains: "Bearer fixture-bearer",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			custom, reserved := "custom", "forbidden"
			testCase.options.Headers = ai.ProviderHeaders{
				"x-fixture": &custom, "authorization": &reserved, "x-amz-fixture": &reserved,
			}
			headers, formatted := runBedrockHTTPFailure(t, testCase.options)
			if !strings.Contains(headers.Get("authorization"), testCase.authContains) {
				t.Fatalf("authorization = %q, want substring %q (error: %s)", headers.Get("authorization"), testCase.authContains, formatted)
			}
			if headers.Get("x-fixture") != custom || headers.Get("x-amz-fixture") != "" {
				t.Fatalf("custom/reserved headers = %#v", headers)
			}
			if !strings.Contains(formatted, "403: denied by fixture gateway") {
				t.Fatalf("formatted error = %q", formatted)
			}
		})
	}
}

// runBedrockHTTPFailure streams one request against a gateway that rejects it
// and returns the request headers and the adapter's formatted error.
func runBedrockHTTPFailure(t *testing.T, options *api.BedrockConverseStreamOptions) (http.Header, string) {
	t.Helper()
	requests := make(chan http.Header, 1)
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		select {
		case requests <- request.Header.Clone():
		default:
		}
		response.Header().Set("content-type", "text/plain")
		response.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(response, "denied by fixture gateway")
	})
	var server *httptest.Server
	var client aws.HTTPClient
	if options.BearerToken != "" {
		server = httptest.NewTLSServer(handler)
		client = server.Client()
	} else {
		server = httptest.NewServer(handler)
	}
	defer server.Close()
	sdkBackend := backend(client)
	options.Backend = &sdkBackend
	model := bedrockTestModel("amazon.nova-micro-v1:0", "Nova Micro")
	model.BaseURL = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := api.StreamBedrockConverseWithOptions(ctx, model, helloContext(), options)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil {
		t.Fatalf("Bedrock request unexpectedly succeeded: %#v", message)
	}
	select {
	case headers := <-requests:
		return headers, *message.ErrorMessage
	default:
		return http.Header{}, *message.ErrorMessage
	}
}

func TestF2BedrockRequestShaping(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name     string          `json:"name"`
			Model    ai.Model        `json:"model"`
			Context  ai.Context      `json:"context"`
			Options  json.RawMessage `json:"options"`
			Expected *struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"expected,omitempty"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "bedrock-requests.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			if fixtureCase.Expected == nil {
				t.Fatal("request fixture has no expected request")
			}
			type capturedRequest struct {
				method, url string
				headers     http.Header
				body        []byte
			}
			capturedRequests := make(chan capturedRequest, 1)
			server := http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Error(err)
					return
				}
				select {
				case capturedRequests <- capturedRequest{method: request.Method, url: request.URL.RequestURI(), headers: request.Header.Clone(), body: body}:
				default:
				}
				response.Header().Set("content-type", "application/json")
				response.Header().Set("x-amzn-errortype", "ValidationException")
				response.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(response, `{"message":"fixture capture complete"}`)
			})}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			serveDone := make(chan error, 1)
			go func() { serveDone <- server.Serve(listener) }()
			t.Cleanup(func() {
				_ = server.Close()
				if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
					t.Error(err)
				}
			})

			fixtureCase.Model.BaseURL = "http://" + listener.Addr().String()
			var options api.BedrockConverseStreamOptions
			if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
				t.Fatal(err)
			}
			sdkBackend := Backend()
			options.Backend = &sdkBackend
			stream, err := api.StreamBedrockConverseWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
			if err != nil {
				t.Fatal(err)
			}
			for _, streamErr := range stream {
				if streamErr != nil {
					t.Fatal(streamErr)
				}
			}
			captured := <-capturedRequests
			if captured.method != fixtureCase.Expected.Method || captured.url != fixtureCase.Expected.URL {
				t.Fatalf("request = %s %s, want %s %s", captured.method, captured.url, fixtureCase.Expected.Method, fixtureCase.Expected.URL)
			}
			selected := make(map[string]string)
			for _, name := range []string{"content-type", "x-fixture"} {
				if value := captured.headers.Get(name); value != "" {
					selected[name] = value
				}
			}
			if diff := runner.ByteDiff(canonicalJSON(t, fixtureCase.Expected.Headers), canonicalJSON(t, selected)); diff != "" {
				t.Fatalf("headers mismatch:\n%s", diff)
			}
			var wantBody, gotBody any
			if err := json.Unmarshal([]byte(fixtureCase.Expected.Body), &wantBody); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(captured.body, &gotBody); err != nil {
				t.Fatalf("invalid request body %s: %v", captured.body, err)
			}
			if diff := runner.ByteDiff(canonicalJSON(t, wantBody), canonicalJSON(t, gotBody)); diff != "" {
				t.Fatalf("request body mismatch:\n%s\nwant: %s\n got: %s", diff, fixtureCase.Expected.Body, captured.body)
			}
		})
	}
}

func canonicalJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
