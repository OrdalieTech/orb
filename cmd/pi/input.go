package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
	textunicode "golang.org/x/text/encoding/unicode"
)

type cliInputError string

func (err cliInputError) Error() string { return string(err) }

type ProcessedFileArguments struct {
	Text   string
	Images []*ai.ImageContent
}

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
	processed, err := ProcessFileArguments(fileArgs, cwd)
	return processed.Text, err
}

// ProcessFileArguments preserves upstream's split between textual <file>
// references and binary image content blocks.
func ProcessFileArguments(fileArgs []string, cwd string) (ProcessedFileArguments, error) {
	var text strings.Builder
	var images []*ai.ImageContent
	for _, fileArgument := range fileArgs {
		absolutePath, err := tools.ResolveReadPath(fileArgument, cwd)
		if err != nil {
			return ProcessedFileArguments{}, err
		}
		info, err := os.Stat(absolutePath)
		if err != nil {
			if os.IsNotExist(err) {
				return ProcessedFileArguments{}, cliInputError(fmt.Sprintf("File not found: %s", absolutePath))
			}
			return ProcessedFileArguments{}, cliInputError(fmt.Sprintf("Could not read file %s: %v", absolutePath, err))
		}
		if info.Size() == 0 {
			continue
		}
		content, err := os.ReadFile(absolutePath)
		if err != nil {
			return ProcessedFileArguments{}, cliInputError(fmt.Sprintf("Could not read file %s: %v", absolutePath, err))
		}
		if mimeType := tools.DetectSupportedImageMimeType(content); mimeType != "" {
			processed := tools.ProcessImage(content, mimeType, nil)
			text.WriteString(`<file name="`)
			text.WriteString(absolutePath)
			text.WriteString(`">`)
			if processed.OK {
				if len(processed.Hints) > 0 {
					text.WriteString(strings.Join(processed.Hints, "\n"))
				}
				images = append(images, &ai.ImageContent{Data: processed.Data, MimeType: processed.MimeType})
			} else {
				text.WriteString(processed.Message)
			}
			text.WriteString("</file>\n")
			continue
		}
		text.WriteString(`<file name="`)
		text.WriteString(absolutePath)
		text.WriteString("\">\n")
		text.WriteString(decodeCLIUTF8(content))
		text.WriteString("\n</file>\n")
	}
	return ProcessedFileArguments{Text: text.String(), Images: images}, nil
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
	message, _, err := PrepareInitialInput(args, cwd, stdinContent)
	return message, err
}

func PrepareInitialInput(args *CLIArgs, cwd string, stdinContent *string) (*string, []*ai.ImageContent, error) {
	files, err := ProcessFileArguments(args.FileArgs, cwd)
	if err != nil {
		return nil, nil, err
	}
	return BuildInitialMessage(args, stdinContent, files.Text), files.Images, nil
}

func decodeCLIUTF8(data []byte) string {
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes(data)
	return string(decoded)
}
