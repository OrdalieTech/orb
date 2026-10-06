package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// TextInteraction runs a sign-in on plain streams: links, codes and progress
// on out, prompts on errOut, one answer line per prompt on in.
type TextInteraction struct {
	reader *bufio.Reader
	out    io.Writer
	err    io.Writer
	mu     sync.Mutex
}

func NewTextInteraction(in io.Reader, out, errOut io.Writer) *TextInteraction {
	return &TextInteraction{reader: bufio.NewReader(in), out: out, err: errOut}
}

func (interaction *TextInteraction) Prompt(ctx context.Context, prompt AuthPrompt) (string, error) {
	interaction.mu.Lock()
	defer interaction.mu.Unlock()
	_, _ = fmt.Fprintln(interaction.err, prompt.Message)
	if prompt.Type == PromptSelect {
		for index, option := range prompt.Options {
			label := option.Label
			if option.Description != "" {
				label += " — " + option.Description
			}
			_, _ = fmt.Fprintf(interaction.err, "  %d) %s\n", index+1, label)
		}
	}
	type answer struct {
		value string
		err   error
	}
	result := make(chan answer, 1)
	go func() {
		value, err := interaction.reader.ReadString('\n')
		if err != nil && err != io.EOF {
			result <- answer{err: err}
			return
		}
		result <- answer{value: strings.TrimRight(value, "\r\n")}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case resolved := <-result:
		if resolved.err != nil || prompt.Type != PromptSelect {
			return resolved.value, resolved.err
		}
		return SelectAnswer(prompt.Options, resolved.value)
	}
}

func (interaction *TextInteraction) Notify(event AuthEvent) {
	switch event.Type {
	case EventAuthURL:
		if event.Instructions != "" {
			_, _ = fmt.Fprintln(interaction.out, event.Instructions)
		}
		_, _ = fmt.Fprintln(interaction.out, event.URL)
	case EventProgress, EventInfo:
		_, _ = fmt.Fprintln(interaction.out, event.Message)
	case EventDeviceCode:
		_, _ = fmt.Fprintf(interaction.out, "%s\n%s\n", event.VerificationURI, event.UserCode)
	}
}

// SelectAnswer maps a numbered choice (or a literal option id) to the option
// id auth flows expect.
func SelectAnswer(options []PromptOption, answer string) (string, error) {
	trimmed := strings.TrimSpace(answer)
	if number, err := strconv.Atoi(trimmed); err == nil && number >= 1 && number <= len(options) {
		return options[number-1].ID, nil
	}
	for _, option := range options {
		if strings.EqualFold(option.ID, trimmed) {
			return option.ID, nil
		}
	}
	return "", fmt.Errorf("invalid selection %q", trimmed)
}

// JSONLines encodes one JSON value per line, safe for concurrent emitters.
type JSONLines struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewJSONLines(out io.Writer) *JSONLines { return &JSONLines{enc: json.NewEncoder(out)} }

func (o *JSONLines) Emit(v any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = o.enc.Encode(v)
}

// JSONInteraction forwards a sign-in's events and prompts as JSON lines, for
// apps that draw their own screens, and takes one answer line per prompt from
// in until in closes, which cancels the prompts. Flows with a browser callback
// also prompt for a pasted code meanwhile; the callback winning cancels it.
type JSONInteraction struct {
	out     *JSONLines
	answers chan string
	closed  chan struct{}
}

// NewJSONInteraction reads in until it closes or ctx ends.
func NewJSONInteraction(ctx context.Context, in io.Reader, out *JSONLines) *JSONInteraction {
	interaction := &JSONInteraction{out: out, answers: make(chan string), closed: make(chan struct{})}
	go func() {
		// Once in closes, prompts fail: an app that goes away leaves no listener behind.
		defer close(interaction.closed)
		lines := bufio.NewScanner(io.LimitReader(in, 1<<20))
		lines.Buffer(make([]byte, 64<<10), 64<<10)
		for lines.Scan() {
			select {
			case interaction.answers <- lines.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return interaction
}

func (i *JSONInteraction) Notify(event AuthEvent) {
	row := map[string]any{"type": event.Type}
	for key, value := range map[string]any{"message": event.Message, "url": event.URL, "instructions": event.Instructions, "code": event.UserCode, "uri": event.VerificationURI, "expires": event.ExpiresInSeconds} {
		if value != "" && value != 0 {
			row[key] = value
		}
	}
	if len(event.Links) > 0 {
		links := make([]map[string]string, len(event.Links))
		for n, link := range event.Links {
			links[n] = map[string]string{"url": link.URL, "label": link.Label}
		}
		row["links"] = links
	}
	i.out.Emit(row)
}

func (i *JSONInteraction) Prompt(ctx context.Context, prompt AuthPrompt) (string, error) {
	options := make([]map[string]string, len(prompt.Options))
	for n, option := range prompt.Options {
		options[n] = map[string]string{"id": option.ID, "label": option.Label, "description": option.Description}
	}
	i.out.Emit(map[string]any{"type": "prompt", "kind": prompt.Type, "message": prompt.Message, "placeholder": prompt.Placeholder, "options": options})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-i.closed:
		return "", context.Canceled
	case answer := <-i.answers:
		if prompt.Type == PromptSelect {
			return SelectAnswer(prompt.Options, answer)
		}
		return strings.TrimSpace(answer), nil
	}
}
