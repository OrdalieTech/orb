package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	googleVertexExternalAccountExecutableDefaultTimeout = 30 * time.Second
	googleVertexExternalAccountExecutableMinimumTimeout = 5 * time.Second
	googleVertexExternalAccountExecutableMaximumTimeout = 120 * time.Second
)

var googleVertexExternalAccountCommandPartPattern = regexp.MustCompile(`(?:[^\s"]+|"[^"]*")+`)

type googleVertexExternalAccountExecutableResponse struct {
	Version        int             `json:"version"`
	Success        *bool           `json:"success"`
	ExpirationTime *int64          `json:"expiration_time"`
	TokenType      string          `json:"token_type"`
	IDToken        string          `json:"id_token"`
	SAMLResponse   string          `json:"saml_response"`
	Code           json.RawMessage `json:"code"`
	Message        string          `json:"message"`
}

func (adc *googleVertexADC) externalAccountExecutableSubjectToken(
	ctx context.Context,
	config *googleVertexExternalAccountConfig,
) (string, error) {
	if adc.providerEnv("GOOGLE_EXTERNAL_ACCOUNT_ALLOW_EXECUTABLES") != "1" {
		return "", errors.New("pluggable auth executables need to be explicitly allowed by setting GOOGLE_EXTERNAL_ACCOUNT_ALLOW_EXECUTABLES to 1")
	}
	source := config.CredentialSource.Executable
	if source == nil || source.Command == "" {
		return "", errors.New(`no valid Pluggable Auth "credential_source" provided`)
	}
	if googleVertexExternalAccountUTF16Length(config.ServiceAccountImpersonationURL) > 256 {
		return "", fmt.Errorf("URL is too long: %s", config.ServiceAccountImpersonationURL) //nolint:staticcheck // Exact upstream prefix.
	}
	timeout := googleVertexExternalAccountExecutableDefaultTimeout
	if source.TimeoutMillis != nil {
		timeout = time.Duration(*source.TimeoutMillis) * time.Millisecond
		if timeout < googleVertexExternalAccountExecutableMinimumTimeout || timeout > googleVertexExternalAccountExecutableMaximumTimeout {
			return "", fmt.Errorf("timeout must be between %d and %d milliseconds", googleVertexExternalAccountExecutableMinimumTimeout/time.Millisecond, googleVertexExternalAccountExecutableMaximumTimeout/time.Millisecond)
		}
	}

	if source.OutputFile != "" {
		cached, found, err := adc.externalAccountCachedExecutableResponse(source.OutputFile)
		if err != nil {
			return "", err
		}
		if found {
			return adc.externalAccountValidateExecutableResponse(cached, true)
		}
	}

	components, err := googleVertexExternalAccountParseCommand(source.Command)
	if err != nil {
		return "", err
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, components[0], components[1:]...)
	command.Env = adc.externalAccountExecutableEnvironment(config)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()
	if commandContext.Err() == context.DeadlineExceeded {
		return "", errors.New("the executable failed to finish within the timeout specified")
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return "", fmt.Errorf("the executable failed with exit code %d and error message: %s", exitError.ExitCode(), output.String())
		}
		return "", err
	}
	response, err := googleVertexExternalAccountDecodeExecutableResponse(output.Bytes(), "the executable returned an invalid response")
	if err != nil {
		return "", err
	}
	return adc.externalAccountValidateExecutableResponse(response, source.OutputFile != "")
}

func (adc *googleVertexADC) externalAccountCachedExecutableResponse(path string) (googleVertexExternalAccountExecutableResponse, bool, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return googleVertexExternalAccountExecutableResponse{}, false, nil
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return googleVertexExternalAccountExecutableResponse{}, false, nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return googleVertexExternalAccountExecutableResponse{}, false, err
	}
	if len(data) == 0 {
		return googleVertexExternalAccountExecutableResponse{}, false, nil
	}
	response, err := googleVertexExternalAccountDecodeExecutableResponse(data, "the output file contained an invalid response")
	if err != nil {
		return googleVertexExternalAccountExecutableResponse{}, false, err
	}
	if response.Success == nil || !*response.Success || googleVertexExternalAccountExecutableExpired(response, adc.now()) {
		return googleVertexExternalAccountExecutableResponse{}, false, nil
	}
	return response, true, nil
}

func googleVertexExternalAccountDecodeExecutableResponse(data []byte, prefix string) (googleVertexExternalAccountExecutableResponse, error) {
	var response googleVertexExternalAccountExecutableResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return response, fmt.Errorf("%s: %s", prefix, string(data))
	}
	if response.Version == 0 {
		return response, errors.New("executable response must contain a version field")
	}
	if response.Success == nil {
		return response, errors.New("executable response must contain a success field")
	}
	if *response.Success {
		switch response.TokenType {
		case "urn:ietf:params:oauth:token-type:saml2":
			if response.SAMLResponse == "" {
				return response, errors.New("executable response must contain a saml_response field for a SAML token")
			}
		case "urn:ietf:params:oauth:token-type:id_token", "urn:ietf:params:oauth:token-type:jwt":
			if response.IDToken == "" {
				return response, errors.New("executable response must contain an id_token field for an OIDC token")
			}
		default:
			return response, errors.New("executable response contains an unsupported token_type")
		}
		return response, nil
	}
	if googleVertexExternalAccountEmptyJSONValue(response.Code) {
		return response, errors.New("executable response must contain a code field when unsuccessful")
	}
	if response.Message == "" {
		return response, errors.New("executable response must contain a message field when unsuccessful")
	}
	return response, nil
}

func googleVertexExternalAccountEmptyJSONValue(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) ||
		bytes.Equal(trimmed, []byte("0")) || bytes.Equal(trimmed, []byte("false"))
}

func (adc *googleVertexADC) externalAccountValidateExecutableResponse(response googleVertexExternalAccountExecutableResponse, outputFile bool) (string, error) {
	if response.Version > 1 {
		return "", errors.New("version of executable is not currently supported, maximum supported version is 1")
	}
	if response.Success == nil || !*response.Success {
		return "", fmt.Errorf("the executable failed with error code %s and message: %s", googleVertexExternalAccountExecutableCode(response), response.Message)
	}
	if outputFile && (response.ExpirationTime == nil || *response.ExpirationTime == 0) {
		return "", errors.New("the executable response must contain expiration_time for successful responses when output_file is configured")
	}
	if googleVertexExternalAccountExecutableExpired(response, adc.now()) {
		return "", errors.New("executable response is expired")
	}
	if response.TokenType == "urn:ietf:params:oauth:token-type:saml2" {
		return response.SAMLResponse, nil
	}
	return response.IDToken, nil
}

func googleVertexExternalAccountExecutableExpired(response googleVertexExternalAccountExecutableResponse, now time.Time) bool {
	seconds := now.Unix()
	if now.Nanosecond() >= int(500*time.Millisecond) {
		seconds++
	}
	return response.ExpirationTime != nil && *response.ExpirationTime < seconds
}

func googleVertexExternalAccountParseCommand(command string) ([]string, error) {
	components := googleVertexExternalAccountCommandPartPattern.FindAllString(command, -1)
	if len(components) == 0 {
		return nil, fmt.Errorf("provided command %q could not be parsed", command)
	}
	for index, component := range components {
		if len(component) >= 2 && component[0] == '"' && component[len(component)-1] == '"' {
			components[index] = component[1 : len(component)-1]
		}
	}
	return components, nil
}

func (adc *googleVertexADC) externalAccountExecutableEnvironment(config *googleVertexExternalAccountConfig) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		if index := strings.IndexByte(entry, '='); index >= 0 {
			values[entry[:index]] = entry[index+1:]
		}
	}
	if adc.options != nil {
		for name, value := range adc.options.Env {
			values[name] = value
		}
	}
	values["GOOGLE_EXTERNAL_ACCOUNT_AUDIENCE"] = config.Audience
	values["GOOGLE_EXTERNAL_ACCOUNT_TOKEN_TYPE"] = config.SubjectTokenType
	values["GOOGLE_EXTERNAL_ACCOUNT_INTERACTIVE"] = "0"
	if output := config.CredentialSource.Executable.OutputFile; output != "" {
		values["GOOGLE_EXTERNAL_ACCOUNT_OUTPUT_FILE"] = output
	}
	if email := googleVertexExternalAccountImpersonatedEmail(config.ServiceAccountImpersonationURL); email != "" {
		values["GOOGLE_EXTERNAL_ACCOUNT_IMPERSONATED_EMAIL"] = email
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment
}

func googleVertexExternalAccountImpersonatedEmail(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	const marker = "/serviceAccounts/"
	start := strings.LastIndex(endpoint, marker)
	if start < 0 || !strings.HasSuffix(endpoint, ":generateAccessToken") {
		return ""
	}
	start += len(marker)
	return strings.TrimSuffix(endpoint[start:], ":generateAccessToken")
}

func googleVertexExternalAccountUTF16Length(value string) int {
	length := 0
	for _, character := range value {
		length++
		if character > 0xffff {
			length++
		}
	}
	return length
}

func googleVertexExternalAccountExecutableCode(response googleVertexExternalAccountExecutableResponse) string {
	var text string
	if json.Unmarshal(response.Code, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(response.Code, &number) == nil {
		return number.String()
	}
	return strconv.Quote(string(response.Code))
}
