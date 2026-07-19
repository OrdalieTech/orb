package extensions

import "sync"

// The bridge's CustomEditor base class (upstream
// packages/coding-agent/src/modes/interactive/components/custom-editor.ts)
// must construct the real built-in editor, which lives above this package.
// The interactive mode registers its constructor here at startup.
var (
	customEditorBaseMu sync.RWMutex
	customEditorBase   EditorFactory
)

func RegisterCustomEditorBase(factory EditorFactory) {
	customEditorBaseMu.Lock()
	customEditorBase = factory
	customEditorBaseMu.Unlock()
}

func CustomEditorBase() EditorFactory {
	customEditorBaseMu.RLock()
	defer customEditorBaseMu.RUnlock()
	return customEditorBase
}
