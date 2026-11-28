package main

import "testing"

func TestPeerActivityLabel(t *testing.T) {
	tests := []struct {
		name        string
		running     []Running
		wantText    string
		wantWorking int
	}{
		{
			name:        "none running",
			running:     nil,
			wantText:    "0 open",
			wantWorking: 0,
		},
		{
			name: "idle open only",
			running: []Running{
				{Instance: "a", Open: true, Busy: false},
				{Instance: "b", Open: true, Busy: false},
			},
			wantText:    "2 open",
			wantWorking: 0,
		},
		{
			name: "one working",
			running: []Running{
				{Instance: "a", Open: true, Busy: true},
			},
			wantText:    "1 working",
			wantWorking: 1,
		},
		{
			name: "two working and one open",
			running: []Running{
				{Instance: "a", Open: true, Busy: true},
				{Instance: "b", Open: true, Busy: false},
				{Instance: "c", Open: true, Busy: true},
			},
			wantText:    "2 working · 1 open",
			wantWorking: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotWorking := peerActivityLabel(tc.running)
			if gotText != tc.wantText || gotWorking != tc.wantWorking {
				t.Fatalf("peerActivityLabel() = (%q, %d), want (%q, %d)", gotText, gotWorking, tc.wantText, tc.wantWorking)
			}
		})
	}
}

func TestTabBusyLabel(t *testing.T) {
	tests := []struct {
		name string
		tab  Tab
		rows []Row
		want string
	}{
		{
			name: "idle tab",
			tab:  Tab{Busy: false, Status: "thinking"},
			rows: []Row{{Kind: "run", Live: true, Actions: []Action{{Verb: "bash", Live: true}}}},
			want: "",
		},
		{
			name: "busy with no rows",
			tab:  Tab{Busy: true},
			rows: nil,
			want: "working",
		},
		{
			name: "busy with live action",
			tab:  Tab{Busy: true, Status: "running turn"},
			rows: []Row{
				{
					Kind: "run",
					Live: true,
					Actions: []Action{
						{Verb: "read", Live: false},
						{Verb: "bash", Live: true},
					},
				},
			},
			want: "bash · running",
		},
		{
			name: "busy with live run row now text",
			tab:  Tab{Busy: true},
			rows: []Row{
				{Kind: "run", Live: true, Now: "editing theme.go", What: "3 edits"},
			},
			want: "editing theme.go",
		},
		{
			name: "busy with live run row what text",
			tab:  Tab{Busy: true},
			rows: []Row{
				{Kind: "run", Live: true, What: "reading files"},
			},
			want: "reading files",
		},
		{
			name: "busy with status text",
			tab:  Tab{Busy: true, Status: "compacting context"},
			rows: []Row{{Kind: "md", Live: false}},
			want: "compacting context",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tabBusyLabel(tc.tab, tc.rows); got != tc.want {
				t.Fatalf("tabBusyLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCollectLiveActivity(t *testing.T) {
	hueFn := func(peer string) int {
		if peer == "mac-mini" {
			return 2
		}
		return 0
	}

	t.Run("empty idle state", func(t *testing.T) {
		tabs := []Tab{
			{ID: "t1", Title: "idle", Busy: false},
		}
		home := Home{
			Machines: []Machine{
				{ID: "m1", Name: "local", Running: []Running{{Instance: "i1", Busy: false, Open: true}}},
			},
		}
		snap := collectLiveActivity(tabs, nil, home, hueFn)
		if !snap.empty() {
			t.Fatalf("expected empty snapshot, got %+v", snap)
		}
		if got := liveActivitySummary(snap); got != "" {
			t.Fatalf("liveActivitySummary(empty) = %q, want empty", got)
		}
	})

	t.Run("pending inputs, live tools, live subagents, thinking agent, and unopened remote", func(t *testing.T) {
		tabs := []Tab{
			{
				ID:    "t-ask",
				Title: "deploy",
				Peer:  "mac-mini",
				Where: "mac-mini",
				Ask:   &Ask{ID: "a1", Title: "Approve production push?"},
			},
			{
				ID:    "t-tools",
				Title: "tests",
				Peer:  "local",
				Where: "local",
				Busy:  true,
			},
			{
				ID:     "t-think",
				Title:  "planning",
				Peer:   "local",
				Where:  "local",
				Busy:   true,
				Status: "thinking…",
			},
		}
		rows := map[string][]Row{
			"t-tools": {
				{
					Kind: "run",
					Live: true,
					Actions: []Action{
						{Verb: "read", Target: "main.go", Live: false},
						{Verb: "bash", Target: "go test ./...", Live: true},
					},
				},
				{
					Kind: "peer",
					Exchange: &Exchange{
						Scope:  "subagent",
						Who:    "researcher",
						Text:   "inspecting activity_bar.go",
						Steps:  "reading model.go\ngrep ui.Row",
						Status: "working",
						Live:   true,
					},
				},
			},
		}
		home := Home{
			Machines: []Machine{
				{
					ID:   "m-remote",
					Name: "gpu-box",
					Hue:  3,
					Running: []Running{
						{Instance: "inst-1", Label: "train-model", Busy: true, Open: false},
						{Instance: "inst-2", Label: "already-open", Busy: true, Open: true},
						{Instance: "inst-3", Label: "idle-remote", Busy: false, Open: false},
					},
				},
			},
		}

		snap := collectLiveActivity(tabs, rows, home, hueFn)
		if snap.empty() {
			t.Fatal("expected non-empty snapshot")
		}
		if len(snap.Inputs) != 1 {
			t.Fatalf("len(Inputs) = %d, want 1", len(snap.Inputs))
		}
		if snap.Inputs[0].TabID != "t-ask" || snap.Inputs[0].Prompt != "Approve production push?" || snap.Inputs[0].Hue != 2 {
			t.Fatalf("unexpected input: %+v", snap.Inputs[0])
		}

		if len(snap.Agents) != 2 {
			t.Fatalf("len(Agents) = %d, want 2", len(snap.Agents))
		}
		if len(snap.Agents[0].Processes) != 2 {
			t.Fatalf("len(Agents[0].Processes) = %d, want 2", len(snap.Agents[0].Processes))
		}
		if snap.Agents[0].Processes[0].Verb != "bash" || snap.Agents[0].Processes[0].Target != "go test ./..." {
			t.Fatalf("unexpected first process: %+v", snap.Agents[0].Processes[0])
		}
		if snap.Agents[0].Processes[1].Verb != "researcher" || snap.Agents[0].Processes[1].Target != "inspecting activity_bar.go" || snap.Agents[0].Processes[1].Detail != "reading model.go" {
			t.Fatalf("unexpected subagent process: %+v", snap.Agents[0].Processes[1])
		}
		if len(snap.Agents[1].Processes) != 0 || snap.Agents[1].Status != "thinking…" {
			t.Fatalf("unexpected thinking agent: %+v", snap.Agents[1])
		}

		if len(snap.Remotes) != 1 {
			t.Fatalf("len(Remotes) = %d, want 1", len(snap.Remotes))
		}
		if snap.Remotes[0].Instance != "inst-1" || snap.Remotes[0].Peer != "gpu-box" || snap.Remotes[0].Label != "train-model" || snap.Remotes[0].Hue != 3 {
			t.Fatalf("unexpected remote: %+v", snap.Remotes[0])
		}

		wantSummary := "3 running · 1 input · bash: go test ./..."
		if got := liveActivitySummary(snap); got != wantSummary {
			t.Fatalf("liveActivitySummary() = %q, want %q", got, wantSummary)
		}
	})

	t.Run("summary with only input", func(t *testing.T) {
		snap := liveActivitySnapshot{
			Inputs: []liveInput{{TabID: "t1", Prompt: "Confirm deletion?"}},
		}
		if got := liveActivitySummary(snap); got != "1 input · Confirm deletion?" {
			t.Fatalf("liveActivitySummary() = %q, want %q", got, "1 input · Confirm deletion?")
		}
	})

	t.Run("summary with only unopened remote", func(t *testing.T) {
		snap := liveActivitySnapshot{
			Remotes: []liveRemote{{Instance: "i1", Peer: "box", Label: "indexing repo"}},
		}
		if got := liveActivitySummary(snap); got != "1 running · indexing repo" {
			t.Fatalf("liveActivitySummary() = %q, want %q", got, "1 running · indexing repo")
		}
	})
}
