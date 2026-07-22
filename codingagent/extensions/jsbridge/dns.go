package jsbridge

import (
	"context"
	"fmt"
	"net"

	"github.com/grafana/sobek"
)

type dnsLookupOptions struct {
	family int
	all    bool
	order  string
}

type dnsLookupAddress struct {
	address string
	family  int
}

func newDNSPromisesModule(runtime *sobek.Runtime, vm *runtimeVM) *sobek.Object {
	module := runtime.NewObject()
	mustSet(runtime, module, "lookup", func(call sobek.FunctionCall) sobek.Value {
		host, ok := call.Argument(0).Export().(string)
		if !ok {
			panic(nodeArgumentError(runtime, "TypeError", "The \"hostname\" argument must be of type string", "ERR_INVALID_ARG_TYPE"))
		}
		options := parseDNSLookupOptions(runtime, call.Argument(1))
		promise, resolve, reject := runtime.NewPromise()
		ctx := vm.context()
		vm.wg.Add(1)
		go func() {
			defer vm.wg.Done()
			addresses, err := resolveDNSLookup(ctx, host, options)
			vm.post(ctx, true, func(runtime *sobek.Runtime) error {
				if err != nil {
					return reject(dnsLookupErrorValue(runtime, host, err))
				}
				if options.all {
					values := make([]any, len(addresses))
					for index, address := range addresses {
						values[index] = map[string]any{"address": address.address, "family": address.family}
					}
					return resolve(toJS(runtime, values))
				}
				if len(addresses) == 0 {
					return reject(dnsLookupErrorValue(runtime, host, &net.DNSError{Err: "no suitable address found", Name: host, IsNotFound: true}))
				}
				return resolve(toJS(runtime, map[string]any{"address": addresses[0].address, "family": addresses[0].family}))
			})
		}()
		return runtime.ToValue(promise)
	})
	return module
}

func parseDNSLookupOptions(runtime *sobek.Runtime, value sobek.Value) dnsLookupOptions {
	options := dnsLookupOptions{order: "verbatim"}
	if !present(value) {
		return options
	}
	object, isObject := value.(*sobek.Object)
	if !isObject {
		family, numeric := dnsInteger(value)
		if !numeric {
			panic(nodeArgumentError(runtime, "TypeError", "The \"options\" argument must be of type number or object", "ERR_INVALID_ARG_TYPE"))
		}
		options.family = family
		validateDNSFamily(runtime, family)
		return options
	}
	if familyValue := object.Get("family"); present(familyValue) {
		family, numeric := dnsInteger(familyValue)
		if !numeric {
			panic(nodeArgumentError(runtime, "TypeError", "The property 'options.family' must be one of: 0, 4, 6", "ERR_INVALID_ARG_VALUE"))
		}
		options.family = family
		validateDNSFamily(runtime, family)
	}
	if allValue := object.Get("all"); present(allValue) {
		all, boolean := allValue.Export().(bool)
		if !boolean {
			panic(nodeArgumentError(runtime, "TypeError", "The \"options.all\" property must be of type boolean", "ERR_INVALID_ARG_TYPE"))
		}
		options.all = all
	}
	if verbatimValue := object.Get("verbatim"); present(verbatimValue) {
		verbatim, boolean := verbatimValue.Export().(bool)
		if !boolean {
			panic(nodeArgumentError(runtime, "TypeError", "The \"options.verbatim\" property must be of type boolean", "ERR_INVALID_ARG_TYPE"))
		}
		if !verbatim {
			options.order = "ipv4first"
		}
	}
	if orderValue := object.Get("order"); present(orderValue) {
		order, stringValue := orderValue.Export().(string)
		if !stringValue || order != "verbatim" && order != "ipv4first" && order != "ipv6first" {
			panic(nodeArgumentError(runtime, "TypeError", "The property 'options.order' must be one of: 'verbatim', 'ipv4first', 'ipv6first'", "ERR_INVALID_ARG_VALUE"))
		}
		options.order = order
	}
	return options
}

func dnsInteger(value sobek.Value) (int, bool) {
	switch value.Export().(type) {
	case int64, float64:
		integer := value.ToInteger()
		return int(integer), float64(integer) == value.ToFloat()
	default:
		return 0, false
	}
}

func validateDNSFamily(runtime *sobek.Runtime, family int) {
	if family != 0 && family != 4 && family != 6 {
		panic(nodeArgumentError(runtime, "TypeError", fmt.Sprintf("The property 'options.family' must be one of: 0, 4, 6. Received %d", family), "ERR_INVALID_ARG_VALUE"))
	}
}

func resolveDNSLookup(ctx context.Context, host string, options dnsLookupOptions) ([]dnsLookupAddress, error) {
	if parsed := net.ParseIP(host); parsed != nil {
		family := 6
		if parsed.To4() != nil {
			family = 4
		}
		return []dnsLookupAddress{{address: host, family: family}}, nil
	}
	network := "ip"
	switch options.family {
	case 4:
		network = "ip4"
	case 6:
		network = "ip6"
	}
	addresses, err := net.DefaultResolver.LookupIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	result := make([]dnsLookupAddress, 0, len(addresses))
	for _, address := range addresses {
		family := 6
		if address.To4() != nil {
			family = 4
		}
		result = append(result, dnsLookupAddress{address: address.String(), family: family})
	}
	switch options.order {
	case "ipv4first":
		result = dnsAddressesByFamily(result, 4)
	case "ipv6first":
		result = dnsAddressesByFamily(result, 6)
	}
	return result, nil
}

func dnsAddressesByFamily(addresses []dnsLookupAddress, first int) []dnsLookupAddress {
	ordered := make([]dnsLookupAddress, 0, len(addresses))
	for _, address := range addresses {
		if address.family == first {
			ordered = append(ordered, address)
		}
	}
	for _, address := range addresses {
		if address.family != first {
			ordered = append(ordered, address)
		}
	}
	return ordered
}

func dnsLookupErrorValue(runtime *sobek.Runtime, host string, err error) sobek.Value {
	code, errno := "EAI_FAIL", -3004
	if dnsError, ok := err.(*net.DNSError); ok {
		switch {
		case dnsError.IsNotFound:
			code, errno = "ENOTFOUND", -3008
		case dnsError.IsTimeout, dnsError.IsTemporary:
			code, errno = "EAI_AGAIN", -3001
		}
	}
	message := "getaddrinfo " + code + " " + host
	errorObject, newErr := runtime.New(runtime.Get("Error"), runtime.ToValue(message))
	if newErr != nil {
		return runtime.NewTypeError(message)
	}
	mustSet(runtime, errorObject, "code", code)
	mustSet(runtime, errorObject, "errno", errno)
	mustSet(runtime, errorObject, "syscall", "getaddrinfo")
	mustSet(runtime, errorObject, "hostname", host)
	return errorObject
}
