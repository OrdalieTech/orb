package runner_test

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/conformance/runner"
	"github.com/OrdalieTech/pi-go/tui"
)

// F12 component fixtures (WP-420): scripted upstream sessions replayed
// against the Go port. Styles mirror conformance/extract/f12-components.ts.
func f12Dim(s string) string  { return "\x1b[2m" + s + "\x1b[22m" }
func f12Bold(s string) string { return "\x1b[1m" + s + "\x1b[22m" }

var f12SelectListTheme = tui.SelectListTheme{
	SelectedPrefix: func(s string) string { return s },
	SelectedText:   f12Bold,
	Description:    f12Dim,
	ScrollInfo:     f12Dim,
	NoMatch:        f12Dim,
}

var f12EditorTheme = tui.EditorTheme{BorderColor: f12Dim, SelectList: f12SelectListTheme}

var f12SettingsTheme = tui.SettingsListTheme{
	Label: func(t string, selected bool) string {
		if selected {
			return f12Bold(t)
		}
		return t
	},
	Value: func(t string, selected bool) string {
		if selected {
			return f12Bold(t)
		}
		return f12Dim(t)
	},
	Description: f12Dim,
	Cursor:      "→ ",
	Hint:        f12Dim,
}

type f12Terminal struct {
	columns int
	rows    int
}

func (terminal *f12Terminal) Start(func(string), func()) error { return nil }
func (terminal *f12Terminal) Stop() error                      { return nil }
func (terminal *f12Terminal) DrainInput(time.Duration, time.Duration) {
}
func (terminal *f12Terminal) Write(string)              {}
func (terminal *f12Terminal) Columns() int              { return terminal.columns }
func (terminal *f12Terminal) Rows() int                 { return terminal.rows }
func (terminal *f12Terminal) KittyProtocolActive() bool { return false }
func (terminal *f12Terminal) MoveBy(int)                {}
func (terminal *f12Terminal) HideCursor()               {}
func (terminal *f12Terminal) ShowCursor()               {}
func (terminal *f12Terminal) ClearLine()                {}
func (terminal *f12Terminal) ClearFromCursor()          {}
func (terminal *f12Terminal) ClearScreen()              {}
func (terminal *f12Terminal) SetTitle(string)           {}
func (terminal *f12Terminal) SetProgress(bool)          {}

func f12KeyEvent(raw string) tui.KeyEvent {
	return tui.KeyEvent{Raw: raw, Key: tui.ParseKey(raw), Type: tui.KeyEventTypeOf(raw)}
}

type f12Observation struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

type f12Op struct {
	Do    string `json:"do"`
	Data  string `json:"data"`
	Text  string `json:"text"`
	Value int    `json:"value"`
	Width int    `json:"width"`
	ID    string `json:"id"`
}

type f12SettingsOp struct {
	Do    string `json:"do"`
	Data  string `json:"data"`
	ID    string `json:"id"`
	Value string `json:"value"`
	Width int    `json:"width"`
}

func wantObservation[T any](t *testing.T, caseName string, index int, observation f12Observation, got T) {
	t.Helper()
	var want T
	if err := json.Unmarshal(observation.Value, &want); err != nil {
		t.Fatalf("%s observation %d (%s): decode: %v", caseName, index, observation.Kind, err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("%s observation %d (%s):\n got %s\nwant %s", caseName, index, observation.Kind, gotJSON, wantJSON)
	}
}

type observationRecorder struct {
	t        *testing.T
	caseName string
	expected []f12Observation
	next     int
}

func (recorder *observationRecorder) expect(kind string) f12Observation {
	recorder.t.Helper()
	if recorder.next >= len(recorder.expected) {
		recorder.t.Fatalf("%s: ran out of expected observations at %q", recorder.caseName, kind)
	}
	observation := recorder.expected[recorder.next]
	if observation.Kind != kind {
		recorder.t.Fatalf("%s observation %d: got kind %q, want %q", recorder.caseName, recorder.next, kind, observation.Kind)
	}
	recorder.next++
	return observation
}

func (recorder *observationRecorder) done() {
	recorder.t.Helper()
	if recorder.next != len(recorder.expected) {
		recorder.t.Fatalf("%s: %d observations not consumed", recorder.caseName, len(recorder.expected)-recorder.next)
	}
}

func TestF12EditorSessions(t *testing.T) {
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			Name     string `json:"name"`
			Rows     int    `json:"rows"`
			Provider *struct {
				Commands []struct {
					Name                string                 `json:"name"`
					Description         string                 `json:"description"`
					ArgumentHint        string                 `json:"argumentHint"`
					ArgumentCompletions []tui.AutocompleteItem `json:"argumentCompletions"`
				} `json:"commands"`
				Files *struct {
					Dirs  []string `json:"dirs"`
					Files []string `json:"files"`
				} `json:"files"`
			} `json:"provider"`
			Ops          []f12Op          `json:"ops"`
			Observations []f12Observation `json:"observations"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F12", "editor.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) == 0 {
		t.Fatalf("bad fixture header: version %d, %d cases", fixture.SchemaVersion, len(fixture.Cases))
	}

	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			editor := tui.NewEditor(tui.NewTUI(&f12Terminal{columns: 80, rows: fixtureCase.Rows}), f12EditorTheme)
			recorder := &observationRecorder{t: t, caseName: fixtureCase.Name, expected: fixtureCase.Observations}
			editor.OnSubmit = func(text string) {
				wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("submit"), text)
			}

			if fixtureCase.Provider != nil {
				baseDir := t.TempDir()
				if fixtureCase.Provider.Files != nil {
					for _, dir := range fixtureCase.Provider.Files.Dirs {
						if err := os.MkdirAll(filepath.Join(baseDir, dir), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					for _, file := range fixtureCase.Provider.Files.Files {
						path := filepath.Join(baseDir, file)
						if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
							t.Fatal(err)
						}
					}
				}
				commands := make([]tui.SlashCommand, 0, len(fixtureCase.Provider.Commands))
				for _, command := range fixtureCase.Provider.Commands {
					slashCommand := tui.SlashCommand{Name: command.Name, Description: command.Description, ArgumentHint: command.ArgumentHint}
					if command.ArgumentCompletions != nil {
						completions := command.ArgumentCompletions
						slashCommand.GetArgumentCompletions = func(string) []tui.AutocompleteItem { return completions }
					}
					commands = append(commands, slashCommand)
				}
				editor.SetAutocompleteProvider(tui.NewCombinedAutocompleteProvider(commands, baseDir, ""))
			}

			for _, op := range fixtureCase.Ops {
				switch op.Do {
				case "input":
					editor.HandleInput(f12KeyEvent(op.Data))
				case "setText":
					editor.SetText(op.Text)
				case "insertText":
					editor.InsertTextAtCursor(op.Text)
				case "addHistory":
					editor.AddToHistory(op.Text)
				case "setPaddingX":
					editor.SetPaddingX(op.Value)
				case "focus":
					editor.SetFocused(true)
				case "text":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("text"), editor.GetText())
				case "expanded":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("expanded"), editor.GetExpandedText())
				case "showing":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("showing"), editor.IsShowingAutocomplete())
				case "cursor":
					line, col := editor.GetCursor()
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("cursor"), map[string]int{"line": line, "col": col})
				case "render":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("render"), editor.Render(op.Width))
				default:
					t.Fatalf("unknown editor op %q", op.Do)
				}
				if fixtureCase.Provider != nil && (op.Do == "input" || op.Do == "setText") {
					time.Sleep(30 * time.Millisecond)
				}
			}
			recorder.done()
		})
	}
}

func TestF12InputSessions(t *testing.T) {
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			Name         string           `json:"name"`
			Ops          []f12Op          `json:"ops"`
			Observations []f12Observation `json:"observations"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F12", "input.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("no input cases")
	}
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			input := tui.NewInput()
			recorder := &observationRecorder{t: t, caseName: fixtureCase.Name, expected: fixtureCase.Observations}
			input.OnSubmit = func(value string) {
				wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("submit"), value)
			}
			for _, op := range fixtureCase.Ops {
				switch op.Do {
				case "input":
					input.HandleInput(f12KeyEvent(op.Data))
				case "setValue":
					input.SetValue(op.Text)
				case "focus":
					input.SetFocused(true)
				case "value":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("value"), input.GetValue())
				case "cursor":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("cursor"), input.GetCursor())
				case "render":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("render"), input.Render(op.Width))
				default:
					t.Fatalf("unknown input op %q", op.Do)
				}
			}
			recorder.done()
		})
	}
}

func TestF12SelectListSessions(t *testing.T) {
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			Name  string `json:"name"`
			Items []struct {
				Value       string `json:"value"`
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"items"`
			MaxVisible int `json:"maxVisible"`
			Layout     *struct {
				MinPrimaryColumnWidth int `json:"minPrimaryColumnWidth"`
				MaxPrimaryColumnWidth int `json:"maxPrimaryColumnWidth"`
			} `json:"layout"`
			Ops          []f12Op          `json:"ops"`
			Observations []f12Observation `json:"observations"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F12", "select-list.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("no select-list cases")
	}
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			items := make([]tui.SelectItem, len(fixtureCase.Items))
			for index, item := range fixtureCase.Items {
				items[index] = tui.SelectItem{Value: item.Value, Label: item.Label, Description: item.Description}
			}
			layout := tui.SelectListLayoutOptions{}
			if fixtureCase.Layout != nil {
				layout.MinPrimaryColumnWidth = fixtureCase.Layout.MinPrimaryColumnWidth
				layout.MaxPrimaryColumnWidth = fixtureCase.Layout.MaxPrimaryColumnWidth
			}
			list := tui.NewSelectList(items, fixtureCase.MaxVisible, f12SelectListTheme, layout)
			recorder := &observationRecorder{t: t, caseName: fixtureCase.Name, expected: fixtureCase.Observations}
			list.OnSelect = func(item tui.SelectItem) {
				wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("select"), item.Value)
			}
			list.OnCancel = func() {
				wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("cancel"), true)
			}
			for _, op := range fixtureCase.Ops {
				switch op.Do {
				case "input":
					list.HandleInput(f12KeyEvent(op.Data))
				case "setFilter":
					list.SetFilter(op.Text)
				case "setSelectedIndex":
					list.SetSelectedIndex(op.Value)
				case "selected":
					var value *string
					if item, ok := list.GetSelectedItem(); ok {
						value = &item.Value
					}
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("selected"), value)
				case "render":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("render"), list.Render(op.Width))
				default:
					t.Fatalf("unknown select op %q", op.Do)
				}
			}
			recorder.done()
		})
	}
}

func TestF12SettingsListSessions(t *testing.T) {
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			Name  string `json:"name"`
			Items []struct {
				ID           string   `json:"id"`
				Label        string   `json:"label"`
				Description  string   `json:"description"`
				CurrentValue string   `json:"currentValue"`
				Values       []string `json:"values"`
			} `json:"items"`
			MaxVisible   int              `json:"maxVisible"`
			EnableSearch bool             `json:"enableSearch"`
			Ops          []f12SettingsOp  `json:"ops"`
			Observations []f12Observation `json:"observations"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F12", "settings-list.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("no settings-list cases")
	}
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			items := make([]tui.SettingItem, len(fixtureCase.Items))
			for index, item := range fixtureCase.Items {
				items[index] = tui.SettingItem{ID: item.ID, Label: item.Label, Description: item.Description, CurrentValue: item.CurrentValue, Values: item.Values}
			}
			recorder := &observationRecorder{t: t, caseName: fixtureCase.Name, expected: fixtureCase.Observations}
			list := tui.NewSettingsList(items, fixtureCase.MaxVisible, f12SettingsTheme,
				func(id, value string) {
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("change"), id+"="+value)
				},
				func() {
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("cancel"), true)
				},
				tui.SettingsListOptions{EnableSearch: fixtureCase.EnableSearch})
			for _, op := range fixtureCase.Ops {
				switch op.Do {
				case "input":
					list.HandleInput(f12KeyEvent(op.Data))
				case "updateValue":
					list.UpdateValue(op.ID, op.Value)
				case "render":
					wantObservation(t, fixtureCase.Name, recorder.next, recorder.expect("render"), list.Render(op.Width))
				default:
					t.Fatalf("unknown settings op %q", op.Do)
				}
			}
			recorder.done()
		})
	}
}

func TestF12WordWrapChunks(t *testing.T) {
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			Line   string `json:"line"`
			Width  int    `json:"width"`
			Chunks []struct {
				Text       string `json:"text"`
				StartIndex int    `json:"startIndex"`
				EndIndex   int    `json:"endIndex"`
			} `json:"chunks"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F12", "word-wrap.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("no word-wrap cases")
	}
	for _, fixtureCase := range fixture.Cases {
		chunks := tui.WordWrapLine(fixtureCase.Line, fixtureCase.Width)
		if len(chunks) != len(fixtureCase.Chunks) {
			t.Errorf("wordWrapLine(%q, %d): %d chunks, want %d", fixtureCase.Line, fixtureCase.Width, len(chunks), len(fixtureCase.Chunks))
			continue
		}
		for index, want := range fixtureCase.Chunks {
			got := chunks[index]
			if got.Text != want.Text || got.StartIndex != want.StartIndex || got.EndIndex != want.EndIndex {
				t.Errorf("wordWrapLine(%q, %d)[%d] = {%q %d %d}, want {%q %d %d}",
					fixtureCase.Line, fixtureCase.Width, index, got.Text, got.StartIndex, got.EndIndex, want.Text, want.StartIndex, want.EndIndex)
			}
		}
	}
}

func TestF12Fuzzy(t *testing.T) {
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Matches       []struct {
			Query   string  `json:"query"`
			Text    string  `json:"text"`
			Matches bool    `json:"matches"`
			Score   float64 `json:"score"`
		} `json:"matches"`
		Filters []struct {
			Items  []string `json:"items"`
			Query  string   `json:"query"`
			Result []string `json:"result"`
		} `json:"filters"`
	}
	runner.LoadJSON(t, "F12", "fuzzy.json", &fixture)
	if len(fixture.Matches) == 0 || len(fixture.Filters) == 0 {
		t.Fatal("empty fuzzy fixture")
	}
	for _, matchCase := range fixture.Matches {
		got := tui.FuzzyMatchScore(matchCase.Query, matchCase.Text)
		if got.Matches != matchCase.Matches {
			t.Errorf("fuzzyMatch(%q, %q).matches = %v, want %v", matchCase.Query, matchCase.Text, got.Matches, matchCase.Matches)
		}
		if got.Matches && math.Abs(got.Score-matchCase.Score) > 1e-9 {
			t.Errorf("fuzzyMatch(%q, %q).score = %v, want %v", matchCase.Query, matchCase.Text, got.Score, matchCase.Score)
		}
	}
	for _, filterCase := range fixture.Filters {
		got := tui.FuzzyFilter(filterCase.Items, filterCase.Query, func(item string) string { return item })
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(filterCase.Result)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("fuzzyFilter(%v, %q) = %s, want %s", filterCase.Items, filterCase.Query, gotJSON, wantJSON)
		}
	}
}

func TestF12WordNavigation(t *testing.T) {
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			Text     string `json:"text"`
			Cursor   int    `json:"cursor"`
			Backward int    `json:"backward"`
			Forward  int    `json:"forward"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F12", "word-navigation.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("no word-navigation cases")
	}
	for _, navCase := range fixture.Cases {
		if got := tui.FindWordBackward(navCase.Text, navCase.Cursor); got != navCase.Backward {
			t.Errorf("findWordBackward(%q, %d) = %d, want %d", navCase.Text, navCase.Cursor, got, navCase.Backward)
		}
		if got := tui.FindWordForward(navCase.Text, navCase.Cursor); got != navCase.Forward {
			t.Errorf("findWordForward(%q, %d) = %d, want %d", navCase.Text, navCase.Cursor, got, navCase.Forward)
		}
	}
}
