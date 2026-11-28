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
