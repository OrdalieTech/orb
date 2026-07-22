package agent

import "sync"

const missingDefaultStreamFnMessage = "No default stream function configured. Pass streamFn explicitly or call setDefaultStreamFn()."

var defaultStreamFnState struct {
	sync.RWMutex
	streamFn StreamFn
}

// SetDefaultStreamFn configures the fallback used when Agent or the low-level
// loop is called without a stream function. Passing nil clears the fallback.
func SetDefaultStreamFn(streamFn StreamFn) {
	defaultStreamFnState.Lock()
	defaultStreamFnState.streamFn = streamFn
	defaultStreamFnState.Unlock()
}

func getDefaultStreamFn() (StreamFn, error) {
	defaultStreamFnState.RLock()
	streamFn := defaultStreamFnState.streamFn
	defaultStreamFnState.RUnlock()
	if streamFn == nil {
		return nil, upstreamError(missingDefaultStreamFnMessage)
	}
	return streamFn, nil
}
