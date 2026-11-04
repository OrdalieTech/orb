package main

// What `orb app` (bridge/view) sends, as the app draws it; bridge/view's Go types name each field.
// The app declares them itself, as the Android and iOS apps do: importing bridge/view would link
// the whole runtime into a program that only draws.

type State struct {
	Self       string      `json:"self"`
	Up         bool        `json:"up"`
	Tabs       []Tab       `json:"tabs"`
	Claim      *Claim      `json:"claim"`
	Invitation *Invitation `json:"invitation"`
	Joining    string      `json:"joining"`
	Launching  string      `json:"launching"`
	Login      *Login      `json:"login"`
	Acting     bool        `json:"acting"`
	Summary    string      `json:"summary"`
	Latest     string      `json:"latest"`
	Commands   []Command   `json:"commands"`
}

type Tab struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Peer      string    `json:"peer"`
	Where     string    `json:"where"`
	Remote    bool      `json:"remote"`
	CWD       string    `json:"cwd"`
	Busy      bool      `json:"busy"`
	Online    bool      `json:"online"`
	Asleep    bool      `json:"asleep"`
	Loaded    bool      `json:"loaded"`
	Earlier   bool      `json:"earlier"`
	Streaming bool      `json:"streaming"`
	Status    string    `json:"status"`
	Model     string    `json:"model"`
	Thinking  string    `json:"thinking"`
	Levels    []string  `json:"levels"`
	Context   float64   `json:"context"`
	Cost      float64   `json:"cost"`
	Usage     *Usage    `json:"usage"`
	Ask       *Ask      `json:"ask"`
	Slots     []Slot    `json:"slots"`
	Unread    int       `json:"unread"`
	Commands  []Command `json:"commands"`
	Models    []string  `json:"models"`
}

type Usage struct {
	Plan    string   `json:"plan"`
	At      int64    `json:"at"`
	Windows []Window `json:"windows"`
}

type Window struct {
	Name   string  `json:"name"`
	Left   float64 `json:"left"`
	Resets int64   `json:"resets"`
}

type Ask struct {
	ID      string   `json:"id"`
	Owner   string   `json:"owner"`
	Title   string   `json:"title"`
	Message string   `json:"message"`
	Choices []string `json:"choices"`
	Free    bool     `json:"free"`
}

type Slot struct {
	Key   string   `json:"key"`
	Lines []string `json:"lines"`
	Alarm bool     `json:"alarm"`
}

type Command struct {
	Name string `json:"name"`
	Hint string `json:"hint"`
	Now  bool   `json:"now"`
}

type Claim struct {
	Invitation string `json:"invitation"`
	Claimant   string `json:"claimant"`
}

type Invitation struct {
	Code    string `json:"code"`
	Expires int64  `json:"expires"`
}

type Login struct {
	Machine      string  `json:"machine"`
	Provider     string  `json:"provider"`
	State        string  `json:"state"` // starting · browser · code · asking · done · failed
	URL          string  `json:"url"`
	Instructions string  `json:"instructions"`
	Code         string  `json:"code"`
	Detail       string  `json:"detail"`
	Prompt       *Prompt `json:"prompt"`
}

type Prompt struct {
	Kind        string   `json:"kind"`
	Message     string   `json:"message"`
	Placeholder string   `json:"placeholder"`
	Options     []Option `json:"options"`
}

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Home struct {
	Machines []Machine `json:"machines"`
	Entries  []Entry   `json:"entries"`
}

type Machine struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Self      bool      `json:"self"`
	Connected bool      `json:"connected"`
	Version   string    `json:"version"`
	Launch    bool      `json:"launch"`
	Hue       int       `json:"hue"`
	Running   []Running `json:"running"`
	Folders   []Folder  `json:"folders"`
}

type Running struct {
	Instance string `json:"instance"`
	Label    string `json:"label"`
	Busy     bool   `json:"busy"`
}

type Folder struct {
	CWD      string `json:"cwd"`
	Threads  int    `json:"threads"`
	Modified int64  `json:"modified"`
	Live     bool   `json:"live"`
}

type Entry struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	Machine   string `json:"machine"`
	CWD       string `json:"cwd"`
	Modified  int64  `json:"modified"`
	Live      bool   `json:"live"`
	Asks      bool   `json:"asks"`
	Open      bool   `json:"open"`
	Unstored  bool   `json:"unstored"`
	Deletable bool   `json:"deletable"`
}

type Row struct {
	Key     string   `json:"k"`
	Kind    string   `json:"kind"` // you · md · run · note
	Text    string   `json:"text"`
	Via     string   `json:"via"`
	Alarm   bool     `json:"alarm"`
	Images  []string `json:"images"`
	Block   *Block   `json:"block"`
	Live    bool     `json:"live"`
	What    string   `json:"what"`
	Now     string   `json:"now"`
	Failed  int      `json:"failed"`
	Actions []Action `json:"actions"`
}

type Action struct {
	Key    string `json:"k"`
	Verb   string `json:"verb"`
	Target string `json:"target"`
	Result string `json:"result"`
	Live   bool   `json:"live"`
	Failed bool   `json:"failed"`
}

type Block struct {
	Type   string     `json:"type"` // p · h · li · quote · code · art · table · rule
	Level  int        `json:"level"`
	Mark   string     `json:"mark"`
	Depth  int        `json:"depth"`
	Spans  []Span     `json:"spans"`
	Lang   string     `json:"lang"`
	Text   string     `json:"text"`
	Blocks []Block    `json:"blocks"`
	Rows   [][][]Span `json:"rows"`
}

type Span struct {
	Text   string `json:"t"`
	Bold   bool   `json:"b"`
	Italic bool   `json:"i"`
	Code   bool   `json:"c"`
	Strike bool   `json:"s"`
	Href   string `json:"h"`
}

type Provider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Methods []Method `json:"methods"`
	Models  int      `json:"models"`
	Ready   bool     `json:"ready"`
	Holds   string   `json:"holds"`
	Status  string   `json:"status"`
	Source  string   `json:"source"`
}

type Method struct {
	Auth    string `json:"auth"` // oauth · api_key
	Label   string `json:"label"`
	About   string `json:"about"`
	Account bool   `json:"account"`
}

type Account struct {
	Provider     string   `json:"provider"`
	ProviderName string   `json:"provider_name"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Active       bool     `json:"active"`
	Plan         string   `json:"plan"`
	Windows      []Window `json:"windows"`
}

// Plugin is a machine's plugin as `orb plugins list --json` lists it.
type Plugin struct {
	Name    string   `json:"name"`
	On      bool     `json:"on"`
	About   string   `json:"about"`
	Choices []Choice `json:"choices"`
}

// Choice is a plugin setting that takes one of a few values.
type Choice struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
	Value  string   `json:"value"`
}

type Completion struct {
	Text   string `json:"text"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}
