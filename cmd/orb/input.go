package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jstrim"
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
	trimmed := strings.TrimFunc(decodeCLIUTF8(data), jstrim.IsSpace)
	if trimmed == "" {
		return nil, nil
	}
	return stringValue(trimmed), nil
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
			fmt.Fprintf(&text, `<file name="%s">`, absolutePath)
			if processed.OK {
				text.WriteString(strings.Join(processed.Hints, "\n"))
				images = append(images, &ai.ImageContent{Data: processed.Data, MimeType: processed.MimeType})
			} else {
				text.WriteString(processed.Message)
			}
			text.WriteString("</file>\n")
			continue
		}
		fmt.Fprintf(&text, "<file name=\"%s\">\n%s\n</file>\n", absolutePath, decodeCLIUTF8(content))
	}
	return ProcessedFileArguments{Text: text.String(), Images: images}, nil
}

// BuildInitialMessage merges stdin, @file text, and the first CLI message
// without inserting separators. It consumes the first message from args.
func BuildInitialMessage(args *CLIArgs, stdinContent *string, fileText string) string {
	message := fileText
	if stdinContent != nil {
		message = *stdinContent + message
	}
	if len(args.Messages) > 0 {
		message += args.Messages[0]
		args.Messages = args.Messages[1:]
	}
	return message
}

func PrepareInitialInput(args *CLIArgs, cwd string, stdinContent *string) (string, []*ai.ImageContent, error) {
	files, err := ProcessFileArguments(args.FileArgs, cwd)
	if err != nil {
		return "", nil, err
	}
	return BuildInitialMessage(args, stdinContent, files.Text), files.Images, nil
}

func decodeCLIUTF8(data []byte) string {
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes(data)
	return string(decoded)
}
