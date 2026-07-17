package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/OrdalieTech/pi-go/codingagent/tools"
	textunicode "golang.org/x/text/encoding/unicode"
)

type cliInputError string

func (err cliInputError) Error() string { return string(err) }

// ReadPipedStdin reads and trims stdin using the upstream CLI rule.
func ReadPipedStdin(reader io.Reader) (*string, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimFunc(decodeCLIUTF8(data), isJSTrimSpace)
	if trimmed == "" {
		return nil, nil
	}
	return stringValue(trimmed), nil
}

// ProcessTextFileArguments renders @file arguments into upstream file blocks.
func ProcessTextFileArguments(fileArgs []string, cwd string) (string, error) {
	var text strings.Builder
	for _, fileArgument := range fileArgs {
		absolutePath, err := tools.ResolveReadPath(fileArgument, cwd)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(absolutePath)
		if err != nil {
			if os.IsNotExist(err) {
				return "", cliInputError(fmt.Sprintf("File not found: %s", absolutePath))
			}
			return "", cliInputError(fmt.Sprintf("Could not read file %s: %v", absolutePath, err))
		}
		if info.Size() == 0 {
			continue
		}
		content, err := os.ReadFile(absolutePath)
		if err != nil {
			return "", cliInputError(fmt.Sprintf("Could not read file %s: %v", absolutePath, err))
		}
		text.WriteString(`<file name="`)
		text.WriteString(absolutePath)
		text.WriteString("\">\n")
		text.WriteString(decodeCLIUTF8(content))
		text.WriteString("\n</file>\n")
	}
	return text.String(), nil
}

// BuildInitialMessage merges stdin, @file text, and the first CLI message
// without inserting separators. It consumes the first message from args.
func BuildInitialMessage(args *CLIArgs, stdinContent *string, fileText string) *string {
	var parts []string
	if stdinContent != nil {
		parts = append(parts, *stdinContent)
	}
	if fileText != "" {
		parts = append(parts, fileText)
	}
	if len(args.Messages) > 0 {
		parts = append(parts, args.Messages[0])
		args.Messages = args.Messages[1:]
	}
	if len(parts) == 0 {
		return nil
	}
	message := strings.Join(parts, "")
	return &message
}

// PrepareInitialMessage processes text attachments and builds the first turn.
func PrepareInitialMessage(args *CLIArgs, cwd string, stdinContent *string) (*string, error) {
	fileText, err := ProcessTextFileArguments(args.FileArgs, cwd)
	if err != nil {
		return nil, err
	}
	return BuildInitialMessage(args, stdinContent, fileText), nil
}

func decodeCLIUTF8(data []byte) string {
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes(data)
	return string(decoded)
}
