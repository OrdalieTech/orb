package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	envNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envPrefixPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
	configValueCache = struct {
		sync.Mutex
		values map[string]*string
	}{values: make(map[string]*string)}
)

type configPart struct {
	env   bool
	value string
}

func ResolveAuthConfigValue(value string, scopedEnv map[string]string) (string, bool) {
	if strings.HasPrefix(value, "!") {
		configValueCache.Lock()
		defer configValueCache.Unlock()
		cached, exists := configValueCache.values[value]
		if exists {
			if cached == nil {
				return "", false
			}
			return *cached, true
		}
		resolved, ok := executeConfigCommand(value[1:])
		if ok {
			copy := resolved
			configValueCache.values[value] = &copy
		} else {
			configValueCache.values[value] = nil
		}
		return resolved, ok
	}
	return resolveConfigTemplate(parseConfigTemplate(value), scopedEnv)
}

func ResolveAuthConfigValueUncached(value string, scopedEnv map[string]string) (string, bool) {
	if strings.HasPrefix(value, "!") {
		return executeConfigCommandContext(context.Background(), value[1:])
	}
	return resolveConfigTemplate(parseConfigTemplate(value), scopedEnv)
}

// ResolveConfigValue resolves a models.json value without the process-lifetime
// command cache used by stored auth credentials.
func ResolveConfigValue(ctx context.Context, value string, scopedEnv map[string]string) (string, error) {
	if strings.HasPrefix(value, "!") {
		resolved, ok := executeConfigCommandContext(ctx, value[1:])
		if !ok {
			return "", fmt.Errorf("failed to resolve from shell command: %s", value[1:])
		}
		return resolved, nil
	}
	resolved, ok := resolveConfigTemplate(parseConfigTemplate(value), scopedEnv)
	if ok {
		return resolved, nil
	}
	missing := GetMissingConfigValueEnvVarNames(value, scopedEnv)
	if len(missing) > 0 {
		return "", fmt.Errorf("failed to resolve from environment variable: %s", missing[0])
	}
	return "", errors.New("failed to resolve configuration value")
}

func ClearConfigValueCache() {
	configValueCache.Lock()
	defer configValueCache.Unlock()
	configValueCache.values = make(map[string]*string)
}

func GetConfigValueEnvVarName(value string) (string, bool) {
	if strings.HasPrefix(value, "!") {
		return "", false
	}
	parts := parseConfigTemplate(value)
	if len(parts) != 1 || !parts[0].env {
		return "", false
	}
	return parts[0].value, true
}

func GetConfigValueEnvVarNames(value string) []string {
	if strings.HasPrefix(value, "!") {
		return nil
	}
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, part := range parseConfigTemplate(value) {
		if !part.env {
			continue
		}
		if _, exists := seen[part.value]; exists {
			continue
		}
		seen[part.value] = struct{}{}
		result = append(result, part.value)
	}
	return result
}

func GetMissingConfigValueEnvVarNames(value string, scopedEnv map[string]string) []string {
	missing := make([]string, 0)
	for _, name := range GetConfigValueEnvVarNames(value) {
		if scopedEnv[name] == "" && os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func IsCommandConfigValue(value string) bool { return strings.HasPrefix(value, "!") }

func IsConfigValueConfigured(value string, scopedEnv map[string]string) bool {
	return len(GetMissingConfigValueEnvVarNames(value, scopedEnv)) == 0
}

func ResolveConfigValueOrThrow(value, description string, scopedEnv map[string]string) (string, error) {
	if resolved, ok := ResolveAuthConfigValueUncached(value, scopedEnv); ok {
		return resolved, nil
	}
	if IsCommandConfigValue(value) {
		return "", fmt.Errorf("failed to resolve %s from shell command: %s", description, strings.TrimPrefix(value, "!"))
	}
	missing := GetMissingConfigValueEnvVarNames(value, scopedEnv)
	switch len(missing) {
	case 1:
		return "", fmt.Errorf("failed to resolve %s from environment variable: %s", description, missing[0])
	case 0:
		return "", fmt.Errorf("failed to resolve %s", description)
	default:
		return "", fmt.Errorf("failed to resolve %s from environment variables: %s", description, strings.Join(missing, ", "))
	}
}

func ResolveHeaders(headers map[string]string, scopedEnv map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	resolved := make(map[string]string)
	for name, value := range headers {
		if result, ok := ResolveAuthConfigValue(value, scopedEnv); ok && result != "" {
			resolved[name] = result
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

func ResolveHeadersOrThrow(headers map[string]string, description string, scopedEnv map[string]string) (map[string]string, error) {
	if headers == nil {
		return nil, nil
	}
	resolved := make(map[string]string, len(headers))
	for name, value := range headers {
		result, err := ResolveConfigValueOrThrow(value, fmt.Sprintf("%s header %q", description, name), scopedEnv)
		if err != nil {
			return nil, err
		}
		resolved[name] = result
	}
	if len(resolved) == 0 {
		return nil, nil
	}
	return resolved, nil
}

func parseConfigTemplate(value string) []configPart {
	parts := make([]configPart, 0, 4)
	appendLiteral := func(literal string) {
		if literal == "" {
			return
		}
		if len(parts) > 0 && !parts[len(parts)-1].env {
			parts[len(parts)-1].value += literal
			return
		}
		parts = append(parts, configPart{value: literal})
	}
	for index := 0; index < len(value); {
		dollar := strings.IndexByte(value[index:], '$')
		if dollar < 0 {
			appendLiteral(value[index:])
			break
		}
		dollar += index
		appendLiteral(value[index:dollar])
		if dollar+1 >= len(value) {
			appendLiteral("$")
			break
		}
		next := value[dollar+1]
		switch next {
		case '$', '!':
			appendLiteral(string(next))
			index = dollar + 2
		case '{':
			end := strings.IndexByte(value[dollar+2:], '}')
			if end < 0 {
				appendLiteral("$")
				index = dollar + 1
				continue
			}
			end += dollar + 2
			name := value[dollar+2 : end]
			if envNamePattern.MatchString(name) {
				parts = append(parts, configPart{env: true, value: name})
			} else {
				appendLiteral(value[dollar : end+1])
			}
			index = end + 1
		default:
			match := envPrefixPattern.FindString(value[dollar+1:])
			if match == "" {
				appendLiteral("$")
				index = dollar + 1
				continue
			}
			parts = append(parts, configPart{env: true, value: match})
			index = dollar + 1 + len(match)
		}
	}
	return parts
}

func resolveConfigTemplate(parts []configPart, scopedEnv map[string]string) (string, bool) {
	var output strings.Builder
	for _, part := range parts {
		if !part.env {
			output.WriteString(part.value)
			continue
		}
		value := scopedEnv[part.value]
		if value == "" {
			value = os.Getenv(part.value)
		}
		if value == "" {
			return "", false
		}
		output.WriteString(value)
	}
	return output.String(), true
}

func executeConfigCommand(command string) (string, bool) {
	return executeConfigCommandContext(context.Background(), command)
}

func executeConfigCommandContext(parent context.Context, command string) (string, bool) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	process.Stdin = nil
	var stdout bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = nil
	if err := process.Run(); err != nil {
		return "", false
	}
	value := trimJSWhitespace(stdout.String())
	return value, value != ""
}

func trimJSWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch {
		case character >= '\t' && character <= '\r':
			return true
		case character == ' ', character == '\u00a0', character == '\u1680', character == '\u2028', character == '\u2029', character == '\u202f', character == '\u205f', character == '\u3000', character == '\ufeff':
			return true
		case character >= '\u2000' && character <= '\u200a':
			return true
		default:
			return false
		}
	})
}
