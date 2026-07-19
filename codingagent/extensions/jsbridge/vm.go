package jsbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/grafana/sobek"
)

var errInvalidFactory = errors.New("extension does not export a factory function")

type vmRequest struct {
	ctx   context.Context
	call  func(*sobek.Runtime) (any, error)
	reply chan vmResponse
}

type vmResponse struct {
	value any
	err   error
}

type vmPost struct {
	ctx            context.Context
	allowCancelled bool
	call           func(*sobek.Runtime) error
}

type undefinedPromiseResult struct{}

var promiseUndefined = undefinedPromiseResult{}

type vmAsyncContextTracker struct {
	vm    *runtimeVM
	stack []context.Context
}

func (tracker *vmAsyncContextTracker) Grab() any {
	return tracker.vm.activeContext
}

func (tracker *vmAsyncContextTracker) Resumed(value any) {
	tracker.stack = append(tracker.stack, tracker.vm.activeContext)
	if ctx, ok := value.(context.Context); ok {
		tracker.vm.activeContext = ctx
	}
}

func (tracker *vmAsyncContextTracker) Exited() {
	if len(tracker.stack) == 0 {
		tracker.vm.activeContext = nil
		return
	}
	last := len(tracker.stack) - 1
	tracker.vm.activeContext = tracker.stack[last]
	tracker.stack = tracker.stack[:last]
}

type runtimeVM struct {
	entry string
	code  []byte
	cwd   string

	requests chan vmRequest
	posts    chan vmPost
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	rootCtx    context.Context
	rootCancel context.CancelFunc

	mu     sync.Mutex
	nextID int64
	ops    map[int64]context.CancelFunc
	wg     sync.WaitGroup

	factory       sobek.Callable
	activeContext context.Context
	signals       map[*sobek.Object]context.Context
	// themes preserves JS-object identity for Theme values crossing the
	// boundary so ui.setTheme round-trips the original Go theme (upstream
	// Theme-object semantics).
	themes        map[*sobek.Object]extensions.Theme
	eventHandlers map[string][]vmEventHandler
	nextEventID   uint64
}

type vmEventHandler struct {
	id      uint64
	handler sobek.Callable
}

type registrationValue struct {
	vm    *runtimeVM
	value sobek.Value
}

func newRuntimeVM(ctx context.Context, entry string, built artifact, cwd string) (*runtimeVM, error) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	vm := &runtimeVM{
		entry:         entry,
		code:          append([]byte(nil), built.code...),
		cwd:           cwd,
		requests:      make(chan vmRequest),
		posts:         make(chan vmPost, 128),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		rootCtx:       rootCtx,
		rootCancel:    rootCancel,
		ops:           make(map[int64]context.CancelFunc),
		signals:       make(map[*sobek.Object]context.Context),
		themes:        make(map[*sobek.Object]extensions.Theme),
		eventHandlers: make(map[string][]vmEventHandler),
	}
	go vm.run()
	_, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		return nil, vm.evaluate(runtime)
	})
	if err != nil {
		vm.Close()
		return nil, err
	}
	return vm, nil
}

func (vm *runtimeVM) run() {
	runtime := sobek.New()
	runtime.SetAsyncContextTracker(&vmAsyncContextTracker{vm: vm})
	defer func() {
		clear(vm.signals)
		clear(vm.eventHandlers)
		close(vm.done)
	}()
	for {
		select {
		case <-vm.stop:
			vm.cancelAll()
			vm.wg.Wait()
			return
		case post := <-vm.posts:
			_ = vm.runPost(runtime, post)
		case request := <-vm.requests:
			vm.runRequest(runtime, request)
		}
	}
}

func (vm *runtimeVM) runRequest(runtime *sobek.Runtime, request vmRequest) {
	if err := request.ctx.Err(); err != nil {
		request.reply <- vmResponse{err: err}
		return
	}
	previousContext := vm.activeContext
	vm.activeContext = request.ctx
	value, err := request.call(runtime)
	vm.activeContext = previousContext
	request.reply <- vmResponse{value: value, err: err}
}

func (vm *runtimeVM) evaluate(runtime *sobek.Runtime) error {
	module := runtime.NewObject()
	exports := runtime.NewObject()
	if err := module.Set("exports", exports); err != nil {
		return err
	}
	if err := runtime.Set("module", module); err != nil {
		return err
	}
	if err := runtime.Set("exports", exports); err != nil {
		return err
	}
	shims := newShimHost(vm.cwd, vm)
	if err := shims.installGlobals(runtime); err != nil {
		return err
	}
	if err := installAbortController(runtime, vm); err != nil {
		return err
	}
	if err := installRequire(runtime, vm); err != nil {
		return err
	}
	baseRequire, ok := sobek.AssertFunction(runtime.Get("require"))
	if !ok {
		return errors.New("extension module loader is unavailable")
	}
	if err := runtime.Set("require", func(call sobek.FunctionCall) sobek.Value {
		if module, found := shims.resolveModule(runtime, call.Argument(0).String()); found {
			return module
		}
		value, err := baseRequire(sobek.Undefined(), call.Arguments...)
		if err != nil {
			panic(err)
		}
		return value
	}); err != nil {
		return err
	}
	if _, err := runtime.RunScript(vm.entry, string(vm.code)); err != nil {
		return err
	}

	exported := module.Get("exports")
	factory, ok := sobek.AssertFunction(exported)
	if !ok {
		object, isObject := exported.(*sobek.Object)
		if !isObject {
			return errInvalidFactory
		}
		factory, ok = sobek.AssertFunction(object.Get("default"))
		if !ok {
			return errInvalidFactory
		}
	}
	vm.factory = factory
	return nil
}

func (vm *runtimeVM) initialize(ctx context.Context, api extensions.API) error {
	_, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		bridge, err := newExtensionAPI(runtime, vm, api)
		if err != nil {
			return nil, err
		}
		value, err := vm.factory(sobek.Undefined(), bridge)
		if err != nil {
			return nil, err
		}
		_, err = vm.awaitValue(ctx, runtime, value)
		return nil, err
	})
	return err
}

func (vm *runtimeVM) postWithContext(ctx context.Context, call func(*sobek.Runtime) error) bool {
	return vm.post(ctx, false, call)
}

func (vm *runtimeVM) post(ctx context.Context, allowCancelled bool, call func(*sobek.Runtime) error) bool {
	select {
	case <-vm.done:
		return false
	case <-vm.stop:
		return false
	case vm.posts <- vmPost{ctx: ctx, allowCancelled: allowCancelled, call: call}:
		return true
	}
}

func (vm *runtimeVM) runPost(runtime *sobek.Runtime, post vmPost) error {
	if post.call == nil {
		return nil
	}
	if !post.allowCancelled && post.ctx != nil && post.ctx.Err() != nil {
		return post.ctx.Err()
	}
	previousContext := vm.activeContext
	if post.ctx != nil {
		vm.activeContext = post.ctx
	}
	err := post.call(runtime)
	vm.activeContext = previousContext
	return err
}

func (vm *runtimeVM) callback(
	ctx context.Context,
	call func(*sobek.Runtime) (any, error),
) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reply := make(chan vmResponse, 1)
	if !vm.postWithContext(ctx, func(runtime *sobek.Runtime) error {
		value, err := call(runtime)
		reply <- vmResponse{value: value, err: err}
		return nil
	}) {
		return nil, errors.New("extension VM is closed")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-vm.done:
		return nil, errors.New("extension VM is closed")
	case response := <-reply:
		return response.value, response.err
	}
}

func (vm *runtimeVM) context() context.Context {
	if vm.activeContext != nil {
		return vm.activeContext
	}
	return context.Background()
}

func (vm *runtimeVM) promise(
	runtime *sobek.Runtime,
	ctx context.Context,
	operation func(context.Context) (any, error),
) sobek.Value {
	if ctx == nil {
		ctx = vm.context()
	}
	promise, resolve, reject := runtime.NewPromise()
	go func() {
		value, err := operation(ctx)
		vm.post(ctx, true, func(runtime *sobek.Runtime) error {
			if err != nil {
				return reject(runtime.NewGoError(err))
			}
			if _, ok := value.(undefinedPromiseResult); ok {
				return resolve(sobek.Undefined())
			}
			return resolve(toJS(runtime, value))
		})
	}()
	return runtime.ToValue(promise)
}

func (vm *runtimeVM) promiseVoid(
	runtime *sobek.Runtime,
	ctx context.Context,
	operation func(context.Context) error,
) sobek.Value {
	return vm.promise(runtime, ctx, func(ctx context.Context) (any, error) {
		return promiseUndefined, operation(ctx)
	})
}

func (vm *runtimeVM) do(
	ctx context.Context,
	call func(*sobek.Runtime) (any, error),
) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reply := make(chan vmResponse, 1)
	request := vmRequest{ctx: ctx, call: call, reply: reply}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-vm.done:
		return nil, errors.New("extension VM is closed")
	case vm.requests <- request:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-vm.done:
		return nil, errors.New("extension VM is closed")
	case response := <-reply:
		return response.value, response.err
	}
}

func (vm *runtimeVM) hostCall(
	ctx context.Context,
	runtime *sobek.Runtime,
	operation func() (any, error),
) (any, error) {
	if ctx == nil {
		ctx = vm.context()
	}
	reply := make(chan vmResponse, 1)
	go func() {
		value, err := operation()
		reply <- vmResponse{value: value, err: err}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-vm.stop:
			return nil, errors.New("extension VM is closed")
		case response := <-reply:
			return response.value, response.err
		case request := <-vm.requests:
			vm.runRequest(runtime, request)
		}
	}
}

func (vm *runtimeVM) Close() {
	vm.stopOnce.Do(func() {
		vm.rootCancel()
		close(vm.stop)
	})
	<-vm.done
}

func (vm *runtimeVM) postCallback(fn func(*sobek.Runtime)) {
	vm.post(vm.rootCtx, true, func(runtime *sobek.Runtime) error {
		fn(runtime)
		return nil
	})
}

func (vm *runtimeVM) allocID() int64 {
	vm.mu.Lock()
	vm.nextID++
	id := vm.nextID
	vm.mu.Unlock()
	return id
}

func (vm *runtimeVM) cancelAll() {
	vm.mu.Lock()
	for id, cancel := range vm.ops {
		cancel()
		delete(vm.ops, id)
	}
	vm.mu.Unlock()
}

func (vm *runtimeVM) cancelOp(id int64) {
	vm.mu.Lock()
	if cancel, ok := vm.ops[id]; ok {
		cancel()
		delete(vm.ops, id)
	}
	vm.mu.Unlock()
}

func (vm *runtimeVM) awaitValue(ctx context.Context, runtime *sobek.Runtime, value sobek.Value) (sobek.Value, error) {
	if value == nil {
		return sobek.Undefined(), nil
	}
	promise, ok := value.Export().(*sobek.Promise)
	if !ok {
		return value, nil
	}
	for promise.State() == sobek.PromiseStatePending {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-vm.stop:
			return nil, errors.New("extension VM is closed")
		case post := <-vm.posts:
			if err := vm.runPost(runtime, post); err != nil {
				return nil, err
			}
		case request := <-vm.requests:
			vm.runRequest(runtime, request)
		}
	}
	if promise.State() == sobek.PromiseStateRejected {
		return nil, jsValueError(promise.Result())
	}
	return promise.Result(), nil
}

func jsValueError(value sobek.Value) error {
	if value == nil || sobek.IsUndefined(value) || sobek.IsNull(value) {
		return errors.New("JavaScript promise rejected")
	}
	if object, ok := value.(*sobek.Object); ok {
		stack := object.Get("stack")
		if stack != nil && !sobek.IsUndefined(stack) && !sobek.IsNull(stack) {
			return errors.New(stack.String())
		}
	}
	return fmt.Errorf("%s", value.String())
}
