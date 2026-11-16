package lspphpactor

import "time"

// indexingState tracks the indexing of one phpactor session: the first work-done progress that begins with a token
// that the session announced through window/workDoneProgress/create.
type indexingState struct {
	// sessionStart is the time when the adapter prepared the initialize params of the session.
	sessionStart time.Time
	// createdTokens holds the progress tokens that the session announced before its indexing began.
	createdTokens map[string]struct{}
	// token identifies the indexing progress; it is empty before the indexing begins.
	token string
	// ended reports that the indexing progress has ended.
	ended bool
	// changed closes when the indexing begins or ends, or when a new session replaces this one.
	changed chan struct{}
}

// newIndexingState returns the indexing state of a phpactor session that starts now.
func newIndexingState() *indexingState {
	return &indexingState{
		sessionStart:  time.Now(),
		createdTokens: make(map[string]struct{}),
		token:         "",
		ended:         false,
		changed:       make(chan struct{}),
	}
}

// hasBegun reports whether the indexing of the session has begun.
func (st *indexingState) hasBegun() bool {
	return st.token != ""
}

// recordCreate remembers a progress token that the session announced before its indexing began.
func (st *indexingState) recordCreate(token string) {
	if st.hasBegun() {
		return
	}
	st.createdTokens[token] = struct{}{}
}

// recordProgress takes the first announced progress that begins as the indexing of the session, records the end of
// this progress, and wakes the waiting reference searches on each of these changes.
func (st *indexingState) recordProgress(token, kind string) {
	switch kind {
	case progressKindBegin:
		if _, created := st.createdTokens[token]; !created || st.hasBegun() {
			return
		}
		st.token = token
		st.createdTokens = nil
	case progressKindEnd:
		if !st.hasBegun() || st.ended || token != st.token {
			return
		}
		st.ended = true
	default:
		return
	}
	st.signalChange()
}

// signalChange wakes the reference searches that wait on the state so that they read it again.
func (st *indexingState) signalChange() {
	close(st.changed)
	st.changed = make(chan struct{})
}
