package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestGoogleVertexRequestAuthResolution(t *testing.T) {
	for _, name := range []string{
		"GOOGLE_CLOUD_PROJECT",
		"GCLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
		"GOOGLE_CLOUD_API_KEY",
	} {
		t.Setenv(name, "")
	}

	realKey := "  vertex-key  "
	marker := googleVertexCredentialMarker
	placeholder := "<authenticated>"
	emptyAngles := "<>"
	whitespace := "  "
	tests := []struct {
		name    string
		options *GoogleVertexOptions
		want    googleVertexRequestAuth
	}{
		{
			name:    "trimmed API key does not require project or location",
			options: &GoogleVertexOptions{StreamOptions: ai.StreamOptions{APIKey: &realKey}},
			want:    googleVertexRequestAuth{apiKey: "vertex-key"},
		},
		{
			name:    "credential marker selects ADC",
			options: &GoogleVertexOptions{StreamOptions: ai.StreamOptions{APIKey: &marker}, Project: "project", Location: "global"},
			want:    googleVertexRequestAuth{project: "project", location: "global", adc: true},
		},
		{
			name:    "angle placeholder selects ADC",
			options: &GoogleVertexOptions{StreamOptions: ai.StreamOptions{APIKey: &placeholder}, Project: "project", Location: "europe-west4"},
			want:    googleVertexRequestAuth{project: "project", location: "europe-west4", adc: true},
		},
		{
			name:    "empty angle brackets are a real API key",
			options: &GoogleVertexOptions{StreamOptions: ai.StreamOptions{APIKey: &emptyAngles}},
			want:    googleVertexRequestAuth{apiKey: "<>"},
		},
		{
			name: "whitespace key selects scoped ADC environment",
			options: &GoogleVertexOptions{StreamOptions: ai.StreamOptions{
				APIKey: &whitespace,
				Env: ai.ProviderEnv{
					"GOOGLE_CLOUD_PROJECT":  "scoped-project",
					"GOOGLE_CLOUD_LOCATION": "us-central1",
				},
			}},
			want: googleVertexRequestAuth{project: "scoped-project", location: "us-central1", adc: true},
		},
		{
			name: "GCLOUD_PROJECT fallback",
			options: &GoogleVertexOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{
				"GCLOUD_PROJECT":        "legacy-project",
				"GOOGLE_CLOUD_LOCATION": "global",
			}}},
			want: googleVertexRequestAuth{project: "legacy-project", location: "global", adc: true},
		},
		{
			name: "explicit project and location win over scoped environment",
			options: &GoogleVertexOptions{
				StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{
					"GOOGLE_CLOUD_PROJECT":  "scoped-project",
					"GOOGLE_CLOUD_LOCATION": "scoped-location",
				}},
				Project: "explicit-project", Location: "explicit-location",
			},
			want: googleVertexRequestAuth{project: "explicit-project", location: "explicit-location", adc: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveGoogleVertexRequestAuth(test.options)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("resolved auth = %#v, want %#v", got, test.want)
			}
		})
	}

	_, err := resolveGoogleVertexRequestAuth(&GoogleVertexOptions{})
	if err == nil || err.Error() != "Vertex AI requires a project ID. Set GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT or pass project in options." {
		t.Fatalf("missing-project error = %v", err)
	}
	_, err = resolveGoogleVertexRequestAuth(&GoogleVertexOptions{Project: "project"})
	if err == nil || err.Error() != "Vertex AI requires a location. Set GOOGLE_CLOUD_LOCATION or pass location in options." {
		t.Fatalf("missing-location error = %v", err)
	}
}

func TestGoogleVertexRejectsEphemeralAPIKeyBeforeCustomHeaderOverride(t *testing.T) {
	ephemeral := "auth_tokens/fixture"
	override := "ordinary-header-key"
	model := vertexTestModel("gemini-2.5-flash")
	stream, err := StreamGoogleVertexWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello")},
	}}, &GoogleVertexOptions{StreamOptions: ai.StreamOptions{
		APIKey:  &ephemeral,
		Headers: ai.ProviderHeaders{"x-goog-api-key": &override},
	}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage != "Ephemeral tokens are only supported by the live API." {
		t.Fatalf("ephemeral-key result = %#v", message)
	}
}

func TestGoogleVertexEndpointVariants(t *testing.T) {
	apiKey := googleVertexRequestAuth{apiKey: "key"}
	adcGlobal := googleVertexRequestAuth{project: "project", location: "global", adc: true}
	tests := []struct {
		name    string
		baseURL string
		model   string
		auth    googleVertexRequestAuth
		want    string
	}{
		{
			name:  "API key default",
			model: "gemini-3-flash-preview", auth: apiKey,
			want: "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-3-flash-preview:streamGenerateContent?alt=sse",
		},
		{
			name:  "ADC global",
			model: "gemini-3-flash-preview", auth: adcGlobal,
			want: "https://aiplatform.googleapis.com/v1/projects/project/locations/global/publishers/google/models/gemini-3-flash-preview:streamGenerateContent?alt=sse",
		},
		{
			name:  "ADC US multiregion",
			model: "gemini-3-flash-preview", auth: googleVertexRequestAuth{project: "p", location: "us", adc: true},
			want: "https://aiplatform.us.rep.googleapis.com/v1/projects/p/locations/us/publishers/google/models/gemini-3-flash-preview:streamGenerateContent?alt=sse",
		},
		{
			name:  "ADC EU multiregion",
			model: "gemini-3-flash-preview", auth: googleVertexRequestAuth{project: "p", location: "eu", adc: true},
			want: "https://aiplatform.eu.rep.googleapis.com/v1/projects/p/locations/eu/publishers/google/models/gemini-3-flash-preview:streamGenerateContent?alt=sse",
		},
		{
			name:  "ADC regional",
			model: "gemini-3-flash-preview", auth: googleVertexRequestAuth{project: "p", location: "europe-west4", adc: true},
			want: "https://europe-west4-aiplatform.googleapis.com/v1/projects/p/locations/europe-west4/publishers/google/models/gemini-3-flash-preview:streamGenerateContent?alt=sse",
		},
		{
			name:  "publisher model shorthand",
			model: "acme/model/ignored", auth: adcGlobal,
			want: "https://aiplatform.googleapis.com/v1/projects/project/locations/global/publishers/acme/models/model:streamGenerateContent?alt=sse",
		},
		{
			name:  "publisher resource",
			model: "publishers/acme/models/model", auth: adcGlobal,
			want: "https://aiplatform.googleapis.com/v1/projects/project/locations/global/publishers/acme/models/model:streamGenerateContent?alt=sse",
		},
		{
			name:  "full project resource suppresses ADC project prefix",
			model: "projects/other/locations/europe-west4/publishers/google/models/model", auth: adcGlobal,
			want: "https://aiplatform.googleapis.com/v1/projects/other/locations/europe-west4/publishers/google/models/model:streamGenerateContent?alt=sse",
		},
		{
			name:    "custom unversioned collection",
			baseURL: "  https://proxy.example.test/collection/  ", model: "model", auth: adcGlobal,
			want: "https://proxy.example.test/collection/v1/publishers/google/models/model:streamGenerateContent?alt=sse",
		},
		{
			name:    "custom versioned collection",
			baseURL: "https://proxy.example.test/v1/projects/p/locations/global", model: "acme/model", auth: adcGlobal,
			want: "https://proxy.example.test/v1/projects/p/locations/global/publishers/acme/models/model:streamGenerateContent?alt=sse",
		},
		{
			name:    "custom beta version segment",
			baseURL: "https://proxy.example.test/root/v1beta2/collection", model: "model", auth: apiKey,
			want: "https://proxy.example.test/root/v1beta2/collection/publishers/google/models/model:streamGenerateContent?alt=sse",
		},
		{
			name:    "generated location placeholder base is ignored",
			baseURL: "https://{location}-aiplatform.googleapis.com/v1/projects/p/locations/{location}", model: "model",
			auth: googleVertexRequestAuth{project: "p", location: "asia-east1", adc: true},
			want: "https://asia-east1-aiplatform.googleapis.com/v1/projects/p/locations/asia-east1/publishers/google/models/model:streamGenerateContent?alt=sse",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := googleVertexEndpoint(test.baseURL, test.model, test.auth)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("endpoint = %q\nwant     = %q", got, test.want)
			}
		})
	}

	for _, model := range []string{"", "../model", "model?alt=json", "model&other=true"} {
		if _, err := googleVertexEndpoint("", model, apiKey); err == nil {
			t.Fatalf("model %q unexpectedly accepted", model)
		}
	}
}

func TestGoogleVertexWireOrderingAndConfigTransforms(t *testing.T) {
	parameters := googleDecodedParameters{
		Model:    "gemini-3-flash-preview",
		Contents: json.RawMessage(`[{"role":"user","parts":[{"text":"x","fileData":{"displayName":"d","fileUri":"gs://x","mimeType":"text/plain"}}]}]`),
		Config: json.RawMessage(`{
			"imageConfig":{"outputCompressionQuality":80,"outputMimeType":"image/png","prominentPeople":"ALLOW","personGeneration":"ALLOW_ADULT","imageSize":"1K","aspectRatio":"1:1","imageOutputOptions":{"compressionQuality":90,"custom":"kept"}},
			"thinkingConfig":{"includeThoughts":true,"thinkingBudget":128},
			"audioTimestamp":true,
			"speechConfig":"Aoede",
			"mediaResolution":"MEDIA_RESOLUTION_HIGH",
			"responseModalities":["TEXT"],
			"modelSelectionConfig":{"featureSelectionPreference":"PRIORITIZE_QUALITY"},
			"routingConfig":{"autoMode":{}},
			"responseJsonSchema":{"type":"object","const":"x"},
			"responseSchema":{"type":"object","properties":{"x":{"type":"string"}}},
			"responseMimeType":"application/json",
			"seed":7,
			"frequencyPenalty":0.2,
			"presencePenalty":0.1,
			"logprobs":3,
			"responseLogprobs":true,
			"stopSequences":["END"],
			"maxOutputTokens":12,
			"candidateCount":1,
			"topK":40,
			"topP":0.9,
			"temperature":0,
			"serviceTier":"FLEX",
			"modelArmorConfig":{"promptTemplateName":"armor"},
			"cachedContent":"c",
			"labels":{"team":"pi"},
			"toolConfig":{"retrievalConfig":{"latLng":{"latitude":1}},"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}},
			"tools":[{"urlContext":{},"parallelAiSearch":{"searchType":"WEB"},"googleSearchRetrieval":{"dynamicRetrievalConfig":{"mode":"MODE_DYNAMIC"}},"functionDeclarations":[{"name":"f","description":"d","parametersJsonSchema":{"type":"object"},"responseJsonSchema":{"type":"string"}}],"enterpriseWebSearch":{},"codeExecution":{},"googleMaps":{},"googleSearch":{},"computerUse":{"environment":"ENVIRONMENT_BROWSER"},"retrieval":{"vertexAiSearch":{"datastore":"d"}}}],
			"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE","method":"SEVERITY"}],
			"systemInstruction":"system"
		}`),
	}
	wire, err := googleVertexWirePayload(parameters, "project", "global")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ai.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"contents":[{"parts":[{"fileData":{"displayName":"d","fileUri":"gs://x","mimeType":"text/plain"},"text":"x"}],"role":"user"}],"systemInstruction":{"parts":[{"text":"system"}],"role":"user"},"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE","method":"SEVERITY"}],"tools":[{"retrieval":{"vertexAiSearch":{"datastore":"d"}},"computerUse":{"environment":"ENVIRONMENT_BROWSER"},"googleSearch":{},"googleMaps":{},"codeExecution":{},"enterpriseWebSearch":{},"functionDeclarations":[{"description":"d","name":"f","parametersJsonSchema":{"type":"object"},"responseJsonSchema":{"type":"string"}}],"googleSearchRetrieval":{"dynamicRetrievalConfig":{"mode":"MODE_DYNAMIC"}},"parallelAiSearch":{"searchType":"WEB"},"urlContext":{}}],"toolConfig":{"retrievalConfig":{"latLng":{"latitude":1}},"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}},"labels":{"team":"pi"},"cachedContent":"projects/project/locations/global/cachedContents/c","modelArmorConfig":{"promptTemplateName":"armor"},"serviceTier":"FLEX","generationConfig":{"temperature":0,"topP":0.9,"topK":40,"candidateCount":1,"maxOutputTokens":12,"stopSequences":["END"],"responseLogprobs":true,"logprobs":3,"presencePenalty":0.1,"frequencyPenalty":0.2,"seed":7,"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"x":{"type":"STRING"}}},"responseJsonSchema":{"type":"object","const":"x"},"routingConfig":{"autoMode":{}},"modelConfig":{"featureSelectionPreference":"PRIORITIZE_QUALITY"},"responseModalities":["TEXT"],"mediaResolution":"MEDIA_RESOLUTION_HIGH","speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Aoede"}}},"audioTimestamp":true,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":128},"imageConfig":{"aspectRatio":"1:1","imageSize":"1K","personGeneration":"ALLOW_ADULT","prominentPeople":"ALLOW","imageOutputOptions":{"mimeType":"image/png","compressionQuality":90,"custom":"kept"}}}}`
	if string(got) != want {
		t.Fatalf("Vertex wire mismatch\nwant: %s\n got: %s", want, got)
	}
}

func TestGoogleVertexSchemaRouting(t *testing.T) {
	parameters := googleDecodedParameters{
		Model:    "model",
		Contents: json.RawMessage(`[{"role":"user","parts":[{"text":"x"}]}]`),
		Config: json.RawMessage(`{
			"responseSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"},
			"tools":[{"functionDeclarations":[{"name":"f","parameters":{"$schema":"draft","type":"object"},"response":{"type":"string"}}]}]
		}`),
	}
	wire, err := googleVertexWirePayload(parameters, "project", "global")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ai.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"contents":[{"parts":[{"text":"x"}],"role":"user"}],"tools":[{"functionDeclarations":[{"name":"f","parametersJsonSchema":{"$schema":"draft","type":"object"},"response":{"type":"STRING"}}]}],"generationConfig":{"responseJsonSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}}}`
	if string(got) != want {
		t.Fatalf("schema routing mismatch\nwant: %s\n got: %s", want, got)
	}
}

func TestGoogleVertexToolAndPartTransforms(t *testing.T) {
	part, err := googleVertexPart(json.RawMessage(`{
		"videoMetadata":{"startOffset":"1s"},
		"thoughtSignature":"signature",
		"thought":true,
		"text":"text",
		"inlineData":{"mimeType":"image/png","data":"abc"},
		"functionResponse":{"name":"f","response":{"ok":true}},
		"functionCall":{"name":"f","args":{},"partialArgs":["a"],"willContinue":true},
		"fileData":{"displayName":"name","fileUri":"gs://bucket/file","mimeType":"text/plain"},
		"executableCode":{"language":"PYTHON","code":"print(1)"},
		"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"},
		"mediaResolution":{"level":"MEDIA_RESOLUTION_HIGH"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	wantPart := `{"mediaResolution":{"level":"MEDIA_RESOLUTION_HIGH"},"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"},"executableCode":{"language":"PYTHON","code":"print(1)"},"fileData":{"displayName":"name","fileUri":"gs://bucket/file","mimeType":"text/plain"},"functionCall":{"name":"f","args":{},"partialArgs":["a"],"willContinue":true},"functionResponse":{"name":"f","response":{"ok":true}},"inlineData":{"mimeType":"image/png","data":"abc"},"text":"text","thought":true,"thoughtSignature":"signature","videoMetadata":{"startOffset":"1s"}}`
	if string(part) != wantPart {
		t.Fatalf("part transform mismatch\nwant: %s\n got: %s", wantPart, part)
	}

	tests := []struct {
		name     string
		contents string
		config   string
		want     string
	}{
		{
			name:   "file search",
			config: `{"tools":[{"fileSearch":{}}]}`,
			want:   "fileSearch parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:   "MCP servers",
			config: `{"tools":[{"mcpServers":[]}]}`,
			want:   "mcpServers parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:   "function behavior",
			config: `{"tools":[{"functionDeclarations":[{"name":"f","behavior":"BLOCKING"}]}]}`,
			want:   "behavior parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:   "server-side tool invocation",
			config: `{"toolConfig":{"includeServerSideToolInvocations":true}}`,
			want:   "includeServerSideToolInvocations parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:   "enhanced civic answers",
			config: `{"enableEnhancedCivicAnswers":true}`,
			want:   "enableEnhancedCivicAnswers parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:     "part tool call",
			contents: `[{"role":"user","parts":[{"toolCall":{}}]}]`,
			config:   `{}`,
			want:     "toolCall parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:     "part tool response",
			contents: `[{"role":"user","parts":[{"toolResponse":{}}]}]`,
			config:   `{}`,
			want:     "toolResponse parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:     "part metadata",
			contents: `[{"role":"user","parts":[{"partMetadata":{}}]}]`,
			config:   `{}`,
			want:     "partMetadata parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contents := test.contents
			if contents == "" {
				contents = `[{"role":"user","parts":[{"text":"x"}]}]`
			}
			_, err := googleVertexWirePayload(googleDecodedParameters{
				Model: "model", Contents: json.RawMessage(contents), Config: json.RawMessage(test.config),
			}, "project", "global")
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGoogleVertexNullUnsupportedFieldsStillError(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		config   string
		want     string
	}{
		{
			name:     "part tool call",
			contents: `[{"role":"user","parts":[{"text":"x","toolCall":null}]}]`,
			config:   `{}`,
			want:     "toolCall parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:   "file search",
			config: `{"tools":[{"fileSearch":null}]}`,
			want:   "fileSearch parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:   "function behavior",
			config: `{"tools":[{"functionDeclarations":[{"name":"f","behavior":null}]}]}`,
			want:   "behavior parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:   "server-side tool invocation",
			config: `{"toolConfig":{"includeServerSideToolInvocations":null}}`,
			want:   "includeServerSideToolInvocations parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
		{
			name:   "enhanced civic answers",
			config: `{"enableEnhancedCivicAnswers":null}`,
			want:   "enableEnhancedCivicAnswers parameter is not supported in Gemini Enterprise Agent Platform (previously known as Vertex AI).",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contents := test.contents
			if contents == "" {
				contents = `[{"role":"user","parts":[{"text":"x"}]}]`
			}
			_, err := googleVertexWirePayload(googleDecodedParameters{
				Model: "model", Contents: json.RawMessage(contents), Config: json.RawMessage(test.config),
			}, "project", "global")
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGoogleVertexThinkingDiffersFromMLDev(t *testing.T) {
	gemma := vertexTestModel("gemma-4-it")
	vertexDisabled := disabledGoogleVertexThinkingConfig(gemma)
	if vertexDisabled.ThinkingBudget == nil || *vertexDisabled.ThinkingBudget != 0 || vertexDisabled.ThinkingLevel != nil {
		t.Fatalf("Vertex disabled Gemma config = %#v", vertexDisabled)
	}
	mldevDisabled := disabledGoogleThinkingConfig(gemma)
	if mldevDisabled.ThinkingLevel == nil || *mldevDisabled.ThinkingLevel != GoogleThinkingMinimal {
		t.Fatalf("MLDev disabled Gemma config = %#v", mldevDisabled)
	}

	flashLite := vertexTestModel("gemini-2.5-flash-lite")
	if got := googleVertexThinkingBudget(flashLite, ai.ThinkingMinimal, nil); got != 128 {
		t.Fatalf("Vertex flash-lite minimal budget = %d", got)
	}
	if got := googleThinkingBudget(flashLite, ai.ThinkingMinimal, nil); got != 512 {
		t.Fatalf("MLDev flash-lite minimal budget = %d", got)
	}
	custom := 77
	if got := googleVertexThinkingBudget(flashLite, ai.ThinkingHigh, &ai.ThinkingBudgets{High: &custom}); got != 77 {
		t.Fatalf("custom Vertex high budget = %d", got)
	}

	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello")},
	}}
	parameters, err := buildGoogleParameters(gemma, requestContext, &GoogleOptions{Thinking: &GoogleThinkingOptions{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeGoogleParameters(parameters)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := googleVertexWirePayload(decoded, "undefined", "undefined")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ai.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"contents":[{"parts":[{"text":"hello"}],"role":"user"}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`
	if string(got) != want {
		t.Fatalf("disabled Gemma wire = %s, want %s", got, want)
	}
}

func TestGoogleVertexStreamRequestHeadersAndEvents(t *testing.T) {
	model := vertexTestModel("gemini-2.5-flash-lite")
	modelHeaders := map[string]string{
		"Content-Type": "application/model-json",
		"X-Custom":     "model",
	}
	model.Headers = &modelHeaders

	apiKey := "vertex-key"
	overrideKey := "header-key"
	contentType := "application/option-json"
	customHeader := "option"
	temperature := 0.0
	maxTokens := 12.0
	reasoning := ai.ThinkingMinimal
	var capturedURL string
	var capturedHeader http.Header
	var capturedBody []byte
	previousClient := googleHTTPClient
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		capturedURL = request.URL.String()
		capturedHeader = request.Header.Clone()
		var err error
		capturedBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return googleTestResponse("data: {\"responseId\":\"vertex-response\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"thinking\",\"thought\":true,\"thoughtSignature\":\"signature\"},{\"text\":\"hello\"},{\"functionCall\":{\"id\":\"call\",\"name\":\"echo\",\"args\":{\"x\":1}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"cachedContentTokenCount\":2,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":4,\"totalTokenCount\":17}}\n\n"), nil
	})}
	t.Cleanup(func() { googleHTTPClient = previousClient })

	requestContext := ai.Context{
		SystemPrompt: vertexStringPointer("system"),
		Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello")},
		},
	}
	stream, err := StreamSimpleGoogleVertex(context.Background(), model, requestContext, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: &apiKey, Temperature: &temperature, MaxTokens: &maxTokens,
			Headers: ai.ProviderHeaders{
				"Content-Type":   &contentType,
				"X-Custom":       &customHeader,
				"X-Goog-Api-Key": &overrideKey,
			},
		},
		Reasoning: &reasoning,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}

	wantURL := "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-2.5-flash-lite:streamGenerateContent?alt=sse"
	if capturedURL != wantURL {
		t.Fatalf("request URL = %q, want %q", capturedURL, wantURL)
	}
	if got := capturedHeader.Get("Content-Type"); got != contentType {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := capturedHeader.Get("X-Custom"); got != customHeader {
		t.Fatalf("X-Custom = %q", got)
	}
	if got := capturedHeader.Get("X-Goog-Api-Key"); got != overrideKey {
		t.Fatalf("X-Goog-Api-Key = %q", got)
	}
	wantBody := `{"contents":[{"parts":[{"text":"hello"}],"role":"user"}],"systemInstruction":{"parts":[{"text":"system"}],"role":"user"},"generationConfig":{"temperature":0,"maxOutputTokens":12,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":128}}}`
	if string(capturedBody) != wantBody {
		t.Fatalf("request body mismatch\nwant: %s\n got: %s", wantBody, capturedBody)
	}
	if message.API != ai.APIGoogleVertex || message.Provider != "google-vertex" || message.ResponseID == nil || *message.ResponseID != "vertex-response" {
		t.Fatalf("message identity = %#v", message)
	}
	if message.StopReason != ai.StopReasonToolUse || len(message.Content) != 3 {
		t.Fatalf("message content/stop = %#v / %q", message.Content, message.StopReason)
	}
	if message.Usage.Input != 8 || message.Usage.Output != 7 || message.Usage.CacheRead != 2 || message.Usage.Reasoning == nil || *message.Usage.Reasoning != 4 || message.Usage.TotalTokens != 17 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestGoogleVertexADCStreamAuthAndHeaderOverrides(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "application_default_credentials.json")
	credential := `{"type":"authorized_user","client_id":"client","client_secret":"secret","refresh_token":"refresh","quota_project_id":"quota-project"}`
	if err := os.WriteFile(credentialPath, []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}

	previousAuthClient := googleVertexAuthHTTPClient
	previousGoogleClient := googleHTTPClient
	t.Cleanup(func() {
		googleVertexAuthHTTPClient = previousAuthClient
		googleHTTPClient = previousGoogleClient
	})

	tokenCalls := 0
	googleVertexAuthHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		tokenCalls++
		if request.URL.String() != googleVertexTokenURL {
			t.Fatalf("token URL = %q", request.URL)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		if values.Get("client_id") != "client" || values.Get("client_secret") != "secret" || values.Get("refresh_token") != "refresh" || values.Get("grant_type") != "refresh_token" {
			t.Fatalf("token form = %#v", values)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"access_token":"adc-token","expires_in":3600,"token_type":"Bearer"}`)),
		}, nil
	})}

	var vertexRequests []http.Header
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		vertexRequests = append(vertexRequests, request.Header.Clone())
		return googleTestResponse("data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n"), nil
	})}

	baseOptions := func() *GoogleVertexOptions {
		return &GoogleVertexOptions{
			StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_APPLICATION_CREDENTIALS": credentialPath}},
			Project:       "project", Location: "global",
		}
	}
	model := vertexTestModel("gemini-3-flash-preview")
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello")},
	}}
	for index := 0; index < 2; index++ {
		options := baseOptions()
		if index == 1 {
			authorization := "Bearer custom-token"
			quota := "custom-quota"
			options.Headers = ai.ProviderHeaders{
				"Authorization":       &authorization,
				"X-Goog-User-Project": &quota,
			}
		}
		stream, err := StreamGoogleVertexWithOptions(context.Background(), model, requestContext, options)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ai.Collect(stream); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls != 2 {
		t.Fatalf("token calls = %d, want one fresh ADC client per stream", tokenCalls)
	}
	if len(vertexRequests) != 2 {
		t.Fatalf("Vertex requests = %d", len(vertexRequests))
	}
	if got := vertexRequests[0].Get("Authorization"); got != "Bearer adc-token" {
		t.Fatalf("ADC Authorization = %q", got)
	}
	if got := vertexRequests[0].Get("X-Goog-User-Project"); got != "quota-project" {
		t.Fatalf("ADC quota project = %q", got)
	}
	if got := vertexRequests[1].Get("Authorization"); got != "Bearer custom-token" {
		t.Fatalf("custom Authorization = %q", got)
	}
	if got := vertexRequests[1].Get("X-Goog-User-Project"); got != "custom-quota" {
		t.Fatalf("custom quota project = %q", got)
	}
}

func vertexTestModel(id string) *ai.Model {
	return &ai.Model{
		ID: id, Name: id, API: ai.APIGoogleVertex, Provider: "google-vertex",
		Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage},
		ContextWindow: 1_000_000, MaxTokens: 65_536,
	}
}

func vertexStringPointer(value string) *string { return &value }
