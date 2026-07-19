package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
)

func googleRawPresent(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func googleNonNullRaw(value json.RawMessage) json.RawMessage {
	if !googleRawPresent(value) {
		return nil
	}
	return value
}

func googleJSTruthy(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("false")) {
		return false
	}
	switch trimmed[0] {
	case '"':
		var text string
		return json.Unmarshal(trimmed, &text) == nil && text != ""
	case '{', '[':
		return true
	default:
		number, err := strconv.ParseFloat(string(trimmed), 64)
		return err != nil || number != 0
	}
}

func googleSchemaHasDollar(value json.RawMessage) bool {
	if !googleRawPresent(value) {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(value, &object) != nil {
		return false
	}
	_, ok := object["$schema"]
	return ok
}

func googleWireContents(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(value)
	if trimmed[0] != '[' {
		if googleRawHasField(value, "functionCall") || googleRawHasField(value, "functionResponse") {
			return nil, errors.New("To specify functionCall or functionResponse parts, please wrap them in a Content object, specifying the role for them") //nolint:staticcheck // Exact SDK text.
		}
		var content json.RawMessage
		var err error
		if googleRawIsContent(value) {
			content, err = googleWireContent(value)
		} else {
			content, err = googleContentFromParts([]json.RawMessage{value})
		}
		if err != nil {
			return nil, err
		}
		return ai.Marshal([]json.RawMessage{content})
	}
	var values []json.RawMessage
	if err := json.Unmarshal(value, &values); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("contents are required")
	}
	contentArray := googleRawIsContent(values[0])
	contents := make([]json.RawMessage, 0, len(values))
	parts := make([]json.RawMessage, 0, len(values))
	for _, item := range values {
		isContent := googleRawIsContent(item)
		if isContent != contentArray {
			return nil, errors.New("Mixing Content and Parts is not supported, please group the parts into a the appropriate Content objects and specify the roles for them") //nolint:staticcheck // Exact SDK text.
		}
		if isContent {
			content, err := googleWireContent(item)
			if err != nil {
				return nil, err
			}
			contents = append(contents, content)
			continue
		}
		if googleRawHasField(item, "functionCall") || googleRawHasField(item, "functionResponse") {
			return nil, errors.New("To specify functionCall or functionResponse parts, please wrap them, and any other parts, in Content objects as appropriate, specifying the role for them") //nolint:staticcheck // Exact SDK text.
		}
		parts = append(parts, item)
	}
	if !contentArray {
		content, err := googleContentFromParts(parts)
		if err != nil {
			return nil, err
		}
		contents = append(contents, content)
	}
	return ai.Marshal(contents)
}

func googleRawIsContent(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(trimmed, &object) != nil {
		return false
	}
	parts, ok := object["parts"]
	return ok && len(bytes.TrimSpace(parts)) > 0 && bytes.TrimSpace(parts)[0] == '['
}

func googleRawHasField(value json.RawMessage, name string) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(trimmed, &object) != nil {
		return false
	}
	_, ok := object[name]
	return ok
}

func googleWireContent(value json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Parts json.RawMessage `json:"parts"`
		Role  json.RawMessage `json:"role"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(input.Parts, &parts); err != nil {
		return nil, err
	}
	converted := make([]json.RawMessage, len(parts))
	for index, part := range parts {
		wire, err := googleWirePart(part)
		if err != nil {
			return nil, err
		}
		converted[index] = wire
	}
	encodedParts, err := ai.Marshal(converted)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		Parts json.RawMessage `json:"parts,omitempty"`
		Role  json.RawMessage `json:"role,omitempty"`
	}{Parts: encodedParts, Role: googleNonNullRaw(input.Role)})
}

func googleContentFromParts(parts []json.RawMessage) (json.RawMessage, error) {
	if len(parts) == 0 {
		return nil, errors.New("PartListUnion is required") //nolint:staticcheck // Exact SDK text.
	}
	converted, err := googleWireParts(parts)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		Parts []json.RawMessage `json:"parts"`
		Role  string            `json:"role"`
	}{Parts: converted, Role: "user"})
}

func googleWireParts(parts []json.RawMessage) ([]json.RawMessage, error) {
	converted := make([]json.RawMessage, len(parts))
	for index, part := range parts {
		trimmed := bytes.TrimSpace(part)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			return nil, errors.New("PartUnion is required") //nolint:staticcheck // Exact SDK text.
		}
		if trimmed[0] == '"' {
			var text string
			if err := json.Unmarshal(trimmed, &text); err != nil {
				return nil, err
			}
			encoded, err := ai.Marshal(struct {
				Text string `json:"text"`
			}{Text: text})
			if err != nil {
				return nil, err
			}
			converted[index] = encoded
			continue
		}
		if trimmed[0] != '{' && trimmed[0] != '[' {
			return nil, fmt.Errorf("Unsupported part type: %s", googleJSONType(string(trimmed))) //nolint:staticcheck // Exact SDK text.
		}
		wire, err := googleWirePart(trimmed)
		if err != nil {
			return nil, err
		}
		converted[index] = wire
	}
	return converted, nil
}

func googleSafetySettings(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var settings []json.RawMessage
	if err := json.Unmarshal(value, &settings); err != nil {
		return value, nil
	}
	result := make([]json.RawMessage, 0, len(settings))
	for _, setting := range settings {
		var input struct {
			Category  json.RawMessage `json:"category"`
			Method    json.RawMessage `json:"method"`
			Threshold json.RawMessage `json:"threshold"`
		}
		if err := json.Unmarshal(setting, &input); err != nil {
			return nil, err
		}
		if len(input.Method) > 0 {
			return nil, errors.New("method parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
		}
		encoded, err := ai.Marshal(struct {
			Category  json.RawMessage `json:"category,omitempty"`
			Threshold json.RawMessage `json:"threshold,omitempty"`
		}{Category: googleNonNullRaw(input.Category), Threshold: googleNonNullRaw(input.Threshold)})
		if err != nil {
			return nil, err
		}
		result = append(result, encoded)
	}
	return ai.Marshal(result)
}

func googleWirePart(value json.RawMessage) (json.RawMessage, error) {
	if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || trimmed[0] != '{' {
		return json.RawMessage(`{}`), nil
	}
	var input struct {
		MediaResolution     json.RawMessage `json:"mediaResolution"`
		CodeExecutionResult json.RawMessage `json:"codeExecutionResult"`
		ExecutableCode      json.RawMessage `json:"executableCode"`
		FileData            json.RawMessage `json:"fileData"`
		FunctionCall        json.RawMessage `json:"functionCall"`
		FunctionResponse    json.RawMessage `json:"functionResponse"`
		InlineData          json.RawMessage `json:"inlineData"`
		Text                json.RawMessage `json:"text"`
		Thought             json.RawMessage `json:"thought"`
		ThoughtSignature    json.RawMessage `json:"thoughtSignature"`
		VideoMetadata       json.RawMessage `json:"videoMetadata"`
		ToolCall            json.RawMessage `json:"toolCall"`
		ToolResponse        json.RawMessage `json:"toolResponse"`
		PartMetadata        json.RawMessage `json:"partMetadata"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	fileData, err := googleWireFileData(input.FileData)
	if err != nil {
		return nil, err
	}
	functionCall, err := googleWireFunctionCall(input.FunctionCall)
	if err != nil {
		return nil, err
	}
	inlineData, err := googleWireBlob(input.InlineData)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		MediaResolution     json.RawMessage `json:"mediaResolution,omitempty"`
		CodeExecutionResult json.RawMessage `json:"codeExecutionResult,omitempty"`
		ExecutableCode      json.RawMessage `json:"executableCode,omitempty"`
		FileData            json.RawMessage `json:"fileData,omitempty"`
		FunctionCall        json.RawMessage `json:"functionCall,omitempty"`
		FunctionResponse    json.RawMessage `json:"functionResponse,omitempty"`
		InlineData          json.RawMessage `json:"inlineData,omitempty"`
		Text                json.RawMessage `json:"text,omitempty"`
		Thought             json.RawMessage `json:"thought,omitempty"`
		ThoughtSignature    json.RawMessage `json:"thoughtSignature,omitempty"`
		VideoMetadata       json.RawMessage `json:"videoMetadata,omitempty"`
		ToolCall            json.RawMessage `json:"toolCall,omitempty"`
		ToolResponse        json.RawMessage `json:"toolResponse,omitempty"`
		PartMetadata        json.RawMessage `json:"partMetadata,omitempty"`
	}{
		MediaResolution: googleNonNullRaw(input.MediaResolution), CodeExecutionResult: googleNonNullRaw(input.CodeExecutionResult),
		ExecutableCode: googleNonNullRaw(input.ExecutableCode), FileData: fileData, FunctionCall: functionCall,
		FunctionResponse: googleNonNullRaw(input.FunctionResponse), InlineData: inlineData, Text: googleNonNullRaw(input.Text),
		Thought: googleNonNullRaw(input.Thought), ThoughtSignature: googleNonNullRaw(input.ThoughtSignature),
		VideoMetadata: googleNonNullRaw(input.VideoMetadata), ToolCall: googleNonNullRaw(input.ToolCall),
		ToolResponse: googleNonNullRaw(input.ToolResponse), PartMetadata: googleNonNullRaw(input.PartMetadata),
	})
}

func googleWireFileData(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		DisplayName json.RawMessage `json:"displayName"`
		FileURI     json.RawMessage `json:"fileUri"`
		MimeType    json.RawMessage `json:"mimeType"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	if len(input.DisplayName) > 0 {
		return nil, errors.New("displayName parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	return ai.Marshal(struct {
		FileURI  json.RawMessage `json:"fileUri,omitempty"`
		MimeType json.RawMessage `json:"mimeType,omitempty"`
	}{FileURI: googleNonNullRaw(input.FileURI), MimeType: googleNonNullRaw(input.MimeType)})
}

func googleWireBlob(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		Data        json.RawMessage `json:"data"`
		DisplayName json.RawMessage `json:"displayName"`
		MimeType    json.RawMessage `json:"mimeType"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	if len(input.DisplayName) > 0 {
		return nil, errors.New("displayName parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	return ai.Marshal(struct {
		Data     json.RawMessage `json:"data,omitempty"`
		MimeType json.RawMessage `json:"mimeType,omitempty"`
	}{Data: googleNonNullRaw(input.Data), MimeType: googleNonNullRaw(input.MimeType)})
}

func googleWireFunctionCall(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		ID           json.RawMessage `json:"id"`
		Args         json.RawMessage `json:"args"`
		Name         json.RawMessage `json:"name"`
		PartialArgs  json.RawMessage `json:"partialArgs"`
		WillContinue json.RawMessage `json:"willContinue"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	if len(input.PartialArgs) > 0 {
		return nil, errors.New("partialArgs parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	if len(input.WillContinue) > 0 {
		return nil, errors.New("willContinue parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	return ai.Marshal(struct {
		ID   json.RawMessage `json:"id,omitempty"`
		Args json.RawMessage `json:"args,omitempty"`
		Name json.RawMessage `json:"name,omitempty"`
	}{ID: googleNonNullRaw(input.ID), Args: googleNonNullRaw(input.Args), Name: googleNonNullRaw(input.Name)})
}

func googleWireTools(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(value, &tools); err != nil {
		return nil, errors.New("tools is required and must be an array of Tools") //nolint:staticcheck // Exact SDK text.
	}
	result := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		converted, err := googleWireTool(tool)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return ai.Marshal(result)
}

func googleWireTool(value json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Retrieval             json.RawMessage `json:"retrieval"`
		ComputerUse           json.RawMessage `json:"computerUse"`
		FileSearch            json.RawMessage `json:"fileSearch"`
		GoogleSearch          json.RawMessage `json:"googleSearch"`
		GoogleMaps            json.RawMessage `json:"googleMaps"`
		CodeExecution         json.RawMessage `json:"codeExecution"`
		EnterpriseWebSearch   json.RawMessage `json:"enterpriseWebSearch"`
		FunctionDeclarations  json.RawMessage `json:"functionDeclarations"`
		GoogleSearchRetrieval json.RawMessage `json:"googleSearchRetrieval"`
		ParallelAISearch      json.RawMessage `json:"parallelAiSearch"`
		URLContext            json.RawMessage `json:"urlContext"`
		MCPServers            json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	declarations, err := googleFunctionDeclarations(input.FunctionDeclarations)
	if err != nil {
		return nil, err
	}
	if len(input.Retrieval) > 0 {
		return nil, errors.New("retrieval parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	googleSearch, err := googleWireSearch(input.GoogleSearch)
	if err != nil {
		return nil, err
	}
	googleMaps, err := googleWireMaps(input.GoogleMaps)
	if err != nil {
		return nil, err
	}
	if len(input.EnterpriseWebSearch) > 0 {
		return nil, errors.New("enterpriseWebSearch parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	if len(input.ParallelAISearch) > 0 {
		return nil, errors.New("parallelAiSearch parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	return ai.Marshal(struct {
		ComputerUse           json.RawMessage `json:"computerUse,omitempty"`
		FileSearch            json.RawMessage `json:"fileSearch,omitempty"`
		GoogleSearch          json.RawMessage `json:"googleSearch,omitempty"`
		GoogleMaps            json.RawMessage `json:"googleMaps,omitempty"`
		CodeExecution         json.RawMessage `json:"codeExecution,omitempty"`
		FunctionDeclarations  json.RawMessage `json:"functionDeclarations,omitempty"`
		GoogleSearchRetrieval json.RawMessage `json:"googleSearchRetrieval,omitempty"`
		URLContext            json.RawMessage `json:"urlContext,omitempty"`
		MCPServers            json.RawMessage `json:"mcpServers,omitempty"`
	}{
		ComputerUse: googleNonNullRaw(input.ComputerUse), FileSearch: googleNonNullRaw(input.FileSearch),
		GoogleSearch: googleSearch, GoogleMaps: googleMaps, CodeExecution: googleNonNullRaw(input.CodeExecution),
		FunctionDeclarations: declarations, GoogleSearchRetrieval: googleNonNullRaw(input.GoogleSearchRetrieval),
		URLContext: googleNonNullRaw(input.URLContext), MCPServers: googleNonNullRaw(input.MCPServers),
	})
}

func googleWireSearch(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		SearchTypes        json.RawMessage `json:"searchTypes"`
		BlockingConfidence json.RawMessage `json:"blockingConfidence"`
		ExcludeDomains     json.RawMessage `json:"excludeDomains"`
		TimeRangeFilter    json.RawMessage `json:"timeRangeFilter"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	if len(input.BlockingConfidence) > 0 {
		return nil, errors.New("blockingConfidence parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	if len(input.ExcludeDomains) > 0 {
		return nil, errors.New("excludeDomains parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	return ai.Marshal(struct {
		SearchTypes     json.RawMessage `json:"searchTypes,omitempty"`
		TimeRangeFilter json.RawMessage `json:"timeRangeFilter,omitempty"`
	}{SearchTypes: googleNonNullRaw(input.SearchTypes), TimeRangeFilter: googleNonNullRaw(input.TimeRangeFilter)})
}

func googleWireMaps(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		AuthConfig   json.RawMessage `json:"authConfig"`
		EnableWidget json.RawMessage `json:"enableWidget"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	auth, err := googleWireMapsAuth(input.AuthConfig)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		AuthConfig   json.RawMessage `json:"authConfig,omitempty"`
		EnableWidget json.RawMessage `json:"enableWidget,omitempty"`
	}{AuthConfig: auth, EnableWidget: googleNonNullRaw(input.EnableWidget)})
}

func googleWireMapsAuth(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		APIKey                     json.RawMessage `json:"apiKey"`
		APIKeyConfig               json.RawMessage `json:"apiKeyConfig"`
		AuthType                   json.RawMessage `json:"authType"`
		GoogleServiceAccountConfig json.RawMessage `json:"googleServiceAccountConfig"`
		HTTPBasicAuthConfig        json.RawMessage `json:"httpBasicAuthConfig"`
		OAuthConfig                json.RawMessage `json:"oauthConfig"`
		OIDCConfig                 json.RawMessage `json:"oidcConfig"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	unsupported := []struct {
		name  string
		value json.RawMessage
	}{
		{"apiKeyConfig", input.APIKeyConfig}, {"authType", input.AuthType},
		{"googleServiceAccountConfig", input.GoogleServiceAccountConfig}, {"httpBasicAuthConfig", input.HTTPBasicAuthConfig},
		{"oauthConfig", input.OAuthConfig}, {"oidcConfig", input.OIDCConfig},
	}
	for _, field := range unsupported {
		if len(field.value) > 0 {
			return nil, fmt.Errorf("%s parameter is not supported in Gemini API.", field.name) //nolint:staticcheck // Exact SDK text.
		}
	}
	return ai.Marshal(struct {
		APIKey json.RawMessage `json:"apiKey,omitempty"`
	}{APIKey: googleNonNullRaw(input.APIKey)})
}

func googleFunctionDeclarations(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	decoded, err := decodeGoogleOrderedJSON(value)
	if err != nil {
		return nil, err
	}
	declarations, ok := decoded.(googleJSONArray)
	if !ok {
		return nil, errors.New("functionDeclarations must be an array")
	}
	for index, value := range declarations {
		declaration, ok := value.(googleJSONObject)
		if !ok {
			return nil, errors.New("function declaration must be an object")
		}
		if err := normalizeGoogleDeclarationSchema(&declaration, "parameters", "parametersJsonSchema"); err != nil {
			return nil, err
		}
		if err := normalizeGoogleDeclarationSchema(&declaration, "response", "responseJsonSchema"); err != nil {
			return nil, err
		}
		declarations[index] = declaration
	}
	return ai.Marshal(declarations)
}

func normalizeGoogleDeclarationSchema(declaration *googleJSONObject, legacy, jsonName string) error {
	schema, ok := declaration.Value(legacy)
	if !ok || !googleJSONValueTruthy(schema) {
		return nil
	}
	object, ok := schema.(googleJSONObject)
	if !ok {
		return errors.New("Google function schema must be an object") //nolint:staticcheck // Public hook diagnostic.
	}
	if _, hasDollar := object.Value("$schema"); hasDollar {
		jsonSchema, _ := declaration.Value(jsonName)
		if !googleJSONValueTruthy(jsonSchema) {
			declaration.Set(jsonName, object)
			declaration.Delete(legacy)
		}
		return nil
	}
	normalized, err := normalizeGoogleSchema(object)
	if err != nil {
		return err
	}
	declaration.Set(legacy, normalized)
	return nil
}

func googleJSONValueTruthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case json.Number:
		number, err := strconv.ParseFloat(string(value), 64)
		return err != nil || number != 0
	default:
		return true
	}
}

func googleWireToolConfig(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		RetrievalConfig                  json.RawMessage `json:"retrievalConfig"`
		FunctionCallingConfig            json.RawMessage `json:"functionCallingConfig"`
		IncludeServerSideToolInvocations json.RawMessage `json:"includeServerSideToolInvocations"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	functionCalling, err := googleFunctionCallingConfig(input.FunctionCallingConfig)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		RetrievalConfig                  json.RawMessage `json:"retrievalConfig,omitempty"`
		FunctionCallingConfig            json.RawMessage `json:"functionCallingConfig,omitempty"`
		IncludeServerSideToolInvocations json.RawMessage `json:"includeServerSideToolInvocations,omitempty"`
	}{
		RetrievalConfig: googleNonNullRaw(input.RetrievalConfig), FunctionCallingConfig: functionCalling,
		IncludeServerSideToolInvocations: googleNonNullRaw(input.IncludeServerSideToolInvocations),
	})
}

func googleFunctionCallingConfig(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var input struct {
		AllowedFunctionNames json.RawMessage `json:"allowedFunctionNames"`
		Mode                 json.RawMessage `json:"mode"`
		StreamArguments      json.RawMessage `json:"streamFunctionCallArguments"`
	}
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, err
	}
	if len(input.StreamArguments) > 0 {
		return nil, errors.New("streamFunctionCallArguments parameter is not supported in Gemini API.") //nolint:staticcheck // Exact SDK text.
	}
	return ai.Marshal(struct {
		AllowedFunctionNames json.RawMessage `json:"allowedFunctionNames,omitempty"`
		Mode                 json.RawMessage `json:"mode,omitempty"`
	}{AllowedFunctionNames: googleNonNullRaw(input.AllowedFunctionNames), Mode: googleNonNullRaw(input.Mode)})
}

func googleCachedContentName(value json.RawMessage) (json.RawMessage, error) {
	if !googleRawPresent(value) {
		return nil, nil
	}
	var name string
	if err := json.Unmarshal(value, &name); err != nil {
		return nil, errors.New("name must be a string") //nolint:staticcheck // Exact SDK text.
	}
	if !strings.HasPrefix(name, "cachedContents/") && !strings.Contains(name, "/") {
		name = "cachedContents/" + name
	}
	return ai.Marshal(name)
}
