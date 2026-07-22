package jsbridge

import (
	"net/netip"

	"github.com/grafana/sobek"
)

func newNetModule(runtime *sobek.Runtime) *sobek.Object {
	module := runtime.NewObject()
	mustSet(runtime, module, "isIP", func(call sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(ipFamily(call.Argument(0).String()))
	})
	mustSet(runtime, module, "isIPv4", func(call sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(ipFamily(call.Argument(0).String()) == 4)
	})
	mustSet(runtime, module, "isIPv6", func(call sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(ipFamily(call.Argument(0).String()) == 6)
	})
	return module
}

func ipFamily(value string) int {
	address, err := netip.ParseAddr(value)
	if err != nil {
		return 0
	}
	if address.Is4() {
		return 4
	}
	return 6
}
