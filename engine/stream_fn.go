package engine

// Upstream's text names its process-wide setDefaultStreamFn(); Orb has no such
// global (DECISIONS.md P10), so the stream function is always passed explicitly.
const missingDefaultStreamFnMessage = "No default stream function configured. Pass streamFn explicitly or call setDefaultStreamFn()."
