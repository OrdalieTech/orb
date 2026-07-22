package jsbridge

import (
	"github.com/grafana/sobek"
	"golang.org/x/term"
)

func newTTYModule(runtime *sobek.Runtime) *sobek.Object {
	module := runtime.NewObject()
	mustSet(runtime, module, "isatty", func(call sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(terminalFD(int(call.Argument(0).ToInteger())))
	})
	return module
}

func terminalFD(fd int) bool {
	return fd >= 0 && term.IsTerminal(fd)
}
