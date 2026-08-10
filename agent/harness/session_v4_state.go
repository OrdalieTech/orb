package harness

import "strconv"

// sessionV4State replays the v4 mutation log into queryable session state,
// mirroring packages/agent/src/harness/session/state.ts.

type sessionV4State struct {
	sequence  int
	usedIDs   map[string]bool
	entries   []SessionV4Entry
	byID      map[string]int
	records   []SessionV4Record
	openOps   map[string][]SessionV4Record
	laneOrder []string
	lanes     map[string]*string
	log       []SessionV4LogItem
	stats     SessionStats
	name      *string
	labels    map[string]string
}

func newSessionV4State() *sessionV4State {
	return &sessionV4State{
		usedIDs:   make(map[string]bool),
		byID:      make(map[string]int),
		openOps:   make(map[string][]SessionV4Record),
		laneOrder: []string{"main"},
		lanes:     map[string]*string{"main": nil},
		labels:    make(map[string]string),
	}
}

func (state *sessionV4State) nextSequence() int {
	return state.sequence + 1
}

func (state *sessionV4State) lanePointers() []SessionV4LanePointer {
	pointers := make([]SessionV4LanePointer, len(state.laneOrder))
	for index, lane := range state.laneOrder {
		pointers[index] = SessionV4LanePointer{Lane: lane, LeafID: cloneHarnessString(state.lanes[lane])}
	}
	return pointers
}

func (state *sessionV4State) requireLane(lane string) (*string, error) {
	leafID, ok := state.lanes[lane]
	if !ok {
		return nil, newSessionError(SessionErrorInvalidLane, "Lane not found: %s", lane)
	}
	return leafID, nil
}

func (state *sessionV4State) validateNewLane(lane string) error {
	if _, ok := state.lanes[lane]; ok {
		return newSessionError(SessionErrorAlreadyExists, "Lane already exists: %s", lane)
	}
	return nil
}

func (state *sessionV4State) validateTarget(targetID *string) error {
	if targetID != nil {
		if _, ok := state.byID[*targetID]; !ok {
			return newSessionError(SessionErrorNotFound, "Entry not found: %s", *targetID)
		}
	}
	return nil
}

func (state *sessionV4State) validateUnusedID(id string) error {
	if state.usedIDs[id] {
		return newSessionError(SessionErrorAlreadyExists, "Session id already exists: %s", id)
	}
	return nil
}

func invalidV4Mutation(message string) error {
	return newSessionError(SessionErrorInvalidEntry, "Invalid session mutation: %s", message)
}

func (state *sessionV4State) applyMutation(mutation SessionV4Mutation, invalid func(string) error) error {
	if invalid == nil {
		invalid = invalidV4Mutation
	}
	seq := mutation.Seq
	switch mutation.Kind {
	case "entry":
		seq = mutation.Entry.Seq
	case "record":
		seq = mutation.Record.Seq
	}
	if seq != state.sequence+1 {
		return invalid("has non-consecutive seq " + strconv.Itoa(seq))
	}
	switch mutation.Kind {
	case "entry":
		entry := *mutation.Entry
		if state.usedIDs[entry.ID] {
			return invalid("contains duplicate id " + entry.ID)
		}
		if mutation.EntryLane != nil {
			leafID, ok := state.lanes[*mutation.EntryLane]
			if !ok {
				return invalid("references missing lane " + *mutation.EntryLane)
			}
			if !equalV4NullableID(entry.ParentID, leafID) {
				return invalid("does not chain to the lane leaf")
			}
		}
		if entry.ParentID != nil {
			if _, ok := state.byID[*entry.ParentID]; !ok {
				return invalid("references missing parent " + *entry.ParentID)
			}
		}
		state.sequence = seq
		state.usedIDs[entry.ID] = true
		state.byID[entry.ID] = len(state.entries)
		state.entries = append(state.entries, entry)
		if mutation.EntryLane != nil {
			state.lanes[*mutation.EntryLane] = cloneHarnessString(&entry.ID)
		}
		stored := &state.entries[len(state.entries)-1]
		state.log = append(state.log, SessionV4LogItem{Kind: "entry", Seq: seq, Entry: stored})
		if entry.Type == "message" {
			state.stats.MessageCount++
		}
	case "record":
		record := *mutation.Record
		if _, ok := state.lanes[record.Lane]; !ok {
			return invalid("references missing lane " + record.Lane)
		}
		if state.usedIDs[record.ID] {
			return invalid("contains duplicate id " + record.ID)
		}
		state.sequence = seq
		state.usedIDs[record.ID] = true
		state.records = append(state.records, record)
		switch record.Type {
		case "operation_started":
			state.openOps[record.Lane] = append(state.openOps[record.Lane], record)
		case "operation_finished":
			open := state.openOps[record.Lane]
			for index := range open {
				if open[index].ID == record.RunID {
					state.openOps[record.Lane] = append(open[:index:index], open[index+1:]...)
					break
				}
			}
		}
		stored := &state.records[len(state.records)-1]
		state.log = append(state.log, SessionV4LogItem{Kind: "record", Seq: seq, Record: stored})
		if record.Type == "usage" && record.Usage != nil {
			state.stats.CachedTokens += record.Usage.CacheRead
			state.stats.UncachedTokens += record.Usage.Input + record.Usage.CacheWrite
			state.stats.TotalTokens += record.Usage.TotalTokens
			state.stats.CostTotal += record.Usage.CostTotal
		}
	case "lane":
		if mutation.LeafID != nil {
			if _, ok := state.byID[*mutation.LeafID]; !ok {
				return invalid("references missing lane target " + *mutation.LeafID)
			}
		}
		state.sequence = seq
		if _, exists := state.lanes[mutation.Lane]; !exists {
			state.laneOrder = append(state.laneOrder, mutation.Lane)
		}
		state.lanes[mutation.Lane] = cloneHarnessString(mutation.LeafID)
		state.log = append(state.log, SessionV4LogItem{
			Kind: "lane", Seq: seq, Lane: mutation.Lane, LeafID: cloneHarnessString(mutation.LeafID),
		})
	case "fact":
		if mutation.Fact == "label" {
			if _, ok := state.byID[mutation.TargetID]; !ok {
				return invalid("references missing label target " + mutation.TargetID)
			}
		}
		state.sequence = seq
		if mutation.Fact == "name" {
			name := mutation.Name
			state.name = &name
			state.log = append(state.log, SessionV4LogItem{Kind: "fact", Seq: seq, Fact: "name", Name: mutation.Name})
		} else {
			if mutation.Label == nil {
				delete(state.labels, mutation.TargetID)
			} else {
				state.labels[mutation.TargetID] = *mutation.Label
			}
			state.log = append(state.log, SessionV4LogItem{
				Kind: "fact", Seq: seq, Fact: "label",
				TargetID: mutation.TargetID, Label: cloneHarnessString(mutation.Label),
			})
		}
	}
	return nil
}

func equalV4NullableID(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (state *sessionV4State) entry(id string) (SessionV4Entry, bool) {
	index, ok := state.byID[id]
	if !ok {
		return SessionV4Entry{}, false
	}
	return state.entries[index].clone(), true
}

// SessionV4EntryQuery mirrors upstream EntryQuery. A nil Limit means
// unlimited; zero and negative limits are invalid.
type SessionV4EntryQuery struct {
	Type       string
	CustomType string
	Order      string
	Limit      *int
	AfterSeq   *int
}

// SessionV4BranchQuery bounds a branch walk from Start toward the root.
type SessionV4BranchQuery struct {
	SessionV4EntryQuery
	Start      string
	StopAtType string
	StopAtID   string
}

// SessionV4RecordQuery mirrors upstream RecordQuery.
type SessionV4RecordQuery struct {
	Lane          string
	Type          string
	RunID         string
	OperationKind string
	AfterSeq      *int
	Order         string
	Limit         *int
}

// SessionV4LogOptions pages the replayed mutation log.
type SessionV4LogOptions struct {
	AfterSeq *int
	Limit    *int
}

func assertV4Limit(limit *int) error {
	if limit != nil && *limit <= 0 {
		return newSessionError(SessionErrorInvalidQuery, "limit must be a positive integer")
	}
	return nil
}

func assertV4Cursor(afterSeq *int) error {
	if afterSeq != nil && *afterSeq < 0 {
		return newSessionError(SessionErrorInvalidQuery, "cursor sequence must be a non-negative integer")
	}
	return nil
}

func (query SessionV4EntryQuery) matches(entry SessionV4Entry) bool {
	if query.Type != "" && entry.Type != query.Type {
		return false
	}
	if query.CustomType != "" && (entry.Type != "custom" || entry.CustomType != query.CustomType) {
		return false
	}
	if query.AfterSeq != nil {
		if query.Order == "oldestFirst" {
			return entry.Seq > *query.AfterSeq
		}
		return entry.Seq < *query.AfterSeq
	}
	return true
}

func (state *sessionV4State) findEntries(query SessionV4EntryQuery) ([]SessionV4Entry, error) {
	if err := assertV4Limit(query.Limit); err != nil {
		return nil, err
	}
	if err := assertV4Cursor(query.AfterSeq); err != nil {
		return nil, err
	}
	results := make([]SessionV4Entry, 0)
	oldestFirst := query.Order == "oldestFirst"
	for offset := 0; offset < len(state.entries); offset++ {
		index := offset
		if !oldestFirst {
			index = len(state.entries) - 1 - offset
		}
		entry := state.entries[index]
		if !query.matches(entry) {
			continue
		}
		results = append(results, entry.clone())
		if query.Limit != nil && len(results) == *query.Limit {
			break
		}
	}
	return results, nil
}

func (state *sessionV4State) walkToRoot(start string, stopAtID, stopAtType string) ([]SessionV4Entry, error) {
	visited := make(map[string]bool)
	index, ok := state.byID[start]
	if !ok {
		return nil, newSessionError(SessionErrorNotFound, "Entry not found: %s", start)
	}
	path := make([]SessionV4Entry, 0)
	current := state.entries[index]
	for {
		if visited[current.ID] {
			return nil, newSessionError(SessionErrorInvalidEntry, "Session branch contains a cycle at %s", current.ID)
		}
		visited[current.ID] = true
		path = append(path, current)
		if (stopAtID != "" && current.ID == stopAtID) || (stopAtType != "" && current.Type == stopAtType) || current.ParentID == nil {
			return path, nil
		}
		parentID := *current.ParentID
		parentIndex, exists := state.byID[parentID]
		if !exists {
			return nil, newSessionError(SessionErrorInvalidEntry, "Entry not found: %s", parentID)
		}
		current = state.entries[parentIndex]
	}
}

func (state *sessionV4State) findEntriesOnBranch(query SessionV4BranchQuery) ([]SessionV4Entry, error) {
	if err := assertV4Limit(query.Limit); err != nil {
		return nil, err
	}
	if err := assertV4Cursor(query.AfterSeq); err != nil {
		return nil, err
	}
	results := make([]SessionV4Entry, 0)
	if query.Order == "oldestFirst" {
		path, err := state.walkToRoot(query.Start, "", "")
		if err != nil {
			return nil, err
		}
		for index := len(path) - 1; index >= 0; index-- {
			entry := path[index]
			reachedBound := (query.StopAtID != "" && entry.ID == query.StopAtID) ||
				(query.StopAtType != "" && entry.Type == query.StopAtType)
			if query.matches(entry) {
				results = append(results, entry.clone())
			}
			if reachedBound || (query.Limit != nil && len(results) == *query.Limit) {
				break
			}
		}
		return results, nil
	}
	path, err := state.walkToRoot(query.Start, query.StopAtID, query.StopAtType)
	if err != nil {
		return nil, err
	}
	for _, entry := range path {
		if query.matches(entry) {
			results = append(results, entry.clone())
		}
		if query.Limit != nil && len(results) == *query.Limit {
			break
		}
	}
	return results, nil
}

func (query SessionV4RecordQuery) matches(record SessionV4Record) bool {
	if query.Lane != "" && record.Lane != query.Lane {
		return false
	}
	if query.Type != "" && record.Type != query.Type {
		return false
	}
	if query.RunID != "" {
		if record.Type == "operation_started" {
			if record.ID != query.RunID {
				return false
			}
		} else if record.RunID != query.RunID {
			return false
		}
	}
	if query.OperationKind != "" && (record.Type != "operation_started" || record.IntentKind != query.OperationKind) {
		return false
	}
	if query.AfterSeq != nil && record.Seq <= *query.AfterSeq {
		return false
	}
	return true
}

func (state *sessionV4State) findRecords(query SessionV4RecordQuery) ([]SessionV4Record, error) {
	if err := assertV4Limit(query.Limit); err != nil {
		return nil, err
	}
	if err := assertV4Cursor(query.AfterSeq); err != nil {
		return nil, err
	}
	results := make([]SessionV4Record, 0)
	oldestFirst := query.Order == "oldestFirst"
	for offset := 0; offset < len(state.records); offset++ {
		index := offset
		if !oldestFirst {
			index = len(state.records) - 1 - offset
		}
		record := state.records[index]
		if !query.matches(record) {
			continue
		}
		results = append(results, record.clone())
		if query.Limit != nil && len(results) == *query.Limit {
			break
		}
	}
	return results, nil
}

func (state *sessionV4State) findOpenOperations(lane string, limit *int) ([]SessionV4Record, error) {
	if err := assertV4Limit(limit); err != nil {
		return nil, err
	}
	open := state.openOps[lane]
	results := make([]SessionV4Record, 0, len(open))
	for index := len(open) - 1; index >= 0; index-- {
		results = append(results, open[index].clone())
		if limit != nil && len(results) == *limit {
			break
		}
	}
	return results, nil
}

func (state *sessionV4State) logItems(options SessionV4LogOptions) ([]SessionV4LogItem, error) {
	if err := assertV4Limit(options.Limit); err != nil {
		return nil, err
	}
	if err := assertV4Cursor(options.AfterSeq); err != nil {
		return nil, err
	}
	results := make([]SessionV4LogItem, 0)
	for _, item := range state.log {
		if options.AfterSeq != nil && item.Seq <= *options.AfterSeq {
			continue
		}
		results = append(results, item)
		if options.Limit != nil && len(results) == *options.Limit {
			break
		}
	}
	return results, nil
}

// SessionV4ForkOptions selects what a fork copies: the main branch up to a
// message entry, or the whole tree.
type SessionV4ForkOptions struct {
	Scope    string
	EntryID  *string
	Position ForkPosition
}

func (state *sessionV4State) createForkMutations(options SessionV4ForkOptions) ([]SessionV4Mutation, error) {
	var copiedEntries []SessionV4Entry
	var forkLanes []SessionV4LanePointer
	if options.Scope == "tree" {
		entries, err := state.findEntries(SessionV4EntryQuery{Order: "oldestFirst"})
		if err != nil {
			return nil, err
		}
		copiedEntries = entries
		forkLanes = state.lanePointers()
	} else {
		selectedEntryID := options.EntryID
		if selectedEntryID == nil {
			leafID, err := state.requireLane("main")
			if err != nil {
				return nil, err
			}
			selectedEntryID = leafID
		}
		var targetID *string
		if selectedEntryID != nil {
			entry, ok := state.entry(*selectedEntryID)
			if !ok || entry.Type != "message" {
				return nil, newSessionError(SessionErrorInvalidFork, "Fork target is not a message entry: %s", *selectedEntryID)
			}
			position := options.Position
			if position == "" {
				position = ForkBefore
				if options.EntryID == nil {
					position = ForkAt
				}
			}
			if position == ForkAt {
				targetID = &entry.ID
			} else {
				targetID = entry.ParentID
			}
		}
		if targetID != nil {
			entries, err := state.findEntriesOnBranch(SessionV4BranchQuery{
				SessionV4EntryQuery: SessionV4EntryQuery{Order: "oldestFirst"}, Start: *targetID,
			})
			if err != nil {
				return nil, err
			}
			copiedEntries = entries
		} else {
			copiedEntries = []SessionV4Entry{}
		}
		forkLanes = []SessionV4LanePointer{{Lane: "main", LeafID: cloneHarnessString(targetID)}}
	}

	mutations := make([]SessionV4Mutation, 0, len(copiedEntries)+len(forkLanes)+1)
	sequence := 1
	for index := range copiedEntries {
		entry := copiedEntries[index].withSeq(sequence)
		sequence++
		mutations = append(mutations, SessionV4Mutation{Kind: "entry", Seq: entry.Seq, Entry: &entry})
	}
	for _, pointer := range forkLanes {
		mutations = append(mutations, SessionV4Mutation{
			Kind: "lane", Seq: sequence, Lane: pointer.Lane, LeafID: cloneHarnessString(pointer.LeafID),
		})
		sequence++
	}
	if state.name != nil {
		mutations = append(mutations, SessionV4Mutation{Kind: "fact", Seq: sequence, Fact: "name", Name: *state.name})
		sequence++
	}
	for _, entry := range copiedEntries {
		if label, ok := state.labels[entry.ID]; ok {
			labelValue := label
			mutations = append(mutations, SessionV4Mutation{
				Kind: "fact", Seq: sequence, Fact: "label", TargetID: entry.ID, Label: &labelValue,
			})
			sequence++
		}
	}
	return mutations, nil
}
