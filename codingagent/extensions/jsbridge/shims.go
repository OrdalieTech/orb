package jsbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/grafana/sobek"
)

// shimHost provides Node-style module shims backed by Go host functions.
// The vm reference is used to post async work (timers, fetch, fs.watch)
// onto the single VM owner goroutine.
type shimHost struct {
	cwd                      string
	vm                       *runtimeVM
	diagnosticsChannelModule *sobek.Object
	performanceModule        *sobek.Object
}

func newShimHost(cwd string, vm *runtimeVM) *shimHost {
	return &shimHost{cwd: absoluteOrDot(cwd), vm: vm}
}

func (h *shimHost) resolveModule(rt *sobek.Runtime, specifier string) (*sobek.Object, bool) {
	name := strings.TrimPrefix(specifier, "node:")
	switch name {
	case "fs":
		return h.newFSModule(rt), true
	case "fs/promises":
		return h.newFSPromisesModule(rt), true
	case "path", "path/posix":
		return h.newPathModule(rt), true
	case "os":
		return newOSModule(rt), true
	case "process":
		return rt.Get("process").ToObject(rt), true
	case "buffer":
		return newBufferModule(rt), true
	case "url":
		return newURLModule(rt), true
	case "util":
		return newUtilModule(rt), true
	case "child_process":
		return h.newChildProcessModule(rt), true
	case "events":
		return newEventsModule(rt), true
	case "readline":
		return newReadlineModule(rt), true
	case "crypto":
		return newCryptoModule(rt), true
	case "http":
		return h.newHTTPModule(rt), true
	case "https":
		return h.newHTTPSModule(rt), true
	case "module":
		return newModuleModule(rt), true
	case "async_hooks":
		return newAsyncHooksModule(rt, h.vm), true
	case "diagnostics_channel":
		if h.diagnosticsChannelModule == nil {
			h.diagnosticsChannelModule = newDiagnosticsChannelModule(rt)
		}
		return h.diagnosticsChannelModule, true
	case "tty":
		return newTTYModule(rt), true
	case "zlib":
		return newZlibModule(rt, h.vm), true
	case "dns/promises":
		return newDNSPromisesModule(rt, h.vm), true
	case "net":
		return newNetModule(rt), true
	case "perf_hooks":
		return h.performanceHooksModule(rt), true
	default:
		return nil, false
	}
}

func (h *shimHost) installGlobals(rt *sobek.Runtime) error {
	if err := h.installProcess(rt); err != nil {
		return err
	}
	if err := installBuffer(rt); err != nil {
		return err
	}
	if err := h.installFetch(rt); err != nil {
		return err
	}
	if err := installConsole(rt); err != nil {
		return err
	}
	if err := installEncodingGlobals(rt); err != nil {
		return err
	}
	// Node's `global` is identity-equal to globalThis; packages read/write it
	// (isexe checks global.TESTING_WINDOWS at module load).
	if err := rt.Set("global", rt.GlobalObject()); err != nil {
		return err
	}
	if err := rt.Set("performance", h.performanceHooksModule(rt).Get("performance")); err != nil {
		return err
	}
	return h.installTimers(rt)
}

func (h *shimHost) performanceHooksModule(rt *sobek.Runtime) *sobek.Object {
	if h.performanceModule != nil {
		return h.performanceModule
	}
	origin := time.Now()
	performance := rt.NewObject()
	mustSet(rt, performance, "timeOrigin", float64(origin.UnixNano())/float64(time.Millisecond))
	mustSet(rt, performance, "now", func(sobek.FunctionCall) sobek.Value {
		return rt.ToValue(float64(time.Since(origin)) / float64(time.Millisecond))
	})
	module := rt.NewObject()
	mustSet(rt, module, "performance", performance)
	h.performanceModule = module
	return module
}

func (h *shimHost) resolvePath(p string) string {
	// Node fs accepts file: URL objects; ours stringify to their href
	// (file:///abs/path). Recover the filesystem path so the URL from
	// new URL("../x", import.meta.url) resolves instead of joining onto cwd.
	if strings.HasPrefix(p, "file://") {
		if u, err := url.Parse(p); err == nil && u.Path != "" {
			p = u.Path
		}
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(h.cwd, p)
}

// --- path module ---

func (h *shimHost) newPathModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	set := func(name string, fn func(sobek.FunctionCall) sobek.Value) {
		if err := m.Set(name, fn); err != nil {
			panic(rt.NewTypeError(err.Error()))
		}
	}
	set("join", func(call sobek.FunctionCall) sobek.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			parts[i] = a.String()
		}
		return rt.ToValue(posixJoin(parts...))
	})
	set("dirname", func(call sobek.FunctionCall) sobek.Value {
		return rt.ToValue(posixDirname(call.Argument(0).String()))
	})
	set("basename", func(call sobek.FunctionCall) sobek.Value {
		p := call.Argument(0).String()
		base := posixBasename(p)
		if len(call.Arguments) > 1 {
			ext := call.Argument(1).String()
			if ext != "" && strings.HasSuffix(base, ext) {
				// Node quirk: when path has a dir component and ext equals the
				// full basename, the basename is returned unstripped.
				hasDir := strings.Contains(p, "/")
				if !hasDir || base != ext {
					base = base[:len(base)-len(ext)]
				}
			}
		}
		return rt.ToValue(base)
	})
	set("extname", func(call sobek.FunctionCall) sobek.Value {
		return rt.ToValue(posixExtname(call.Argument(0).String()))
	})
	set("resolve", func(call sobek.FunctionCall) sobek.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			parts[i] = a.String()
		}
		return rt.ToValue(posixResolve(h.cwd, parts...))
	})
	set("relative", func(call sobek.FunctionCall) sobek.Value {
		from := call.Argument(0).String()
		to := call.Argument(1).String()
		return rt.ToValue(posixRelative(h.cwd, from, to))
	})
	set("isAbsolute", func(call sobek.FunctionCall) sobek.Value {
		return rt.ToValue(strings.HasPrefix(call.Argument(0).String(), "/"))
	})
	set("normalize", func(call sobek.FunctionCall) sobek.Value {
		return rt.ToValue(posixNormalize(call.Argument(0).String()))
	})
	set("parse", func(call sobek.FunctionCall) sobek.Value {
		p := call.Argument(0).String()
		base := posixBasename(p)
		ext := posixExtname(base)
		name := base
		if ext != "" {
			name = base[:len(base)-len(ext)]
		}
		o := rt.NewObject()
		mustSet(rt, o, "root", posixRoot(p))
		mustSet(rt, o, "dir", posixParseDir(p))
		mustSet(rt, o, "base", base)
		mustSet(rt, o, "ext", ext)
		mustSet(rt, o, "name", name)
		return o
	})
	set("format", func(call sobek.FunctionCall) sobek.Value {
		obj := call.Argument(0).ToObject(rt)
		dir := stringProp(obj, "dir")
		root := stringProp(obj, "root")
		base := stringProp(obj, "base")
		if base == "" {
			ext := stringProp(obj, "ext")
			if ext != "" && ext[0] != '.' {
				ext = "." + ext
			}
			base = stringProp(obj, "name") + ext
		}
		if dir == "" {
			dir = root
		}
		if dir == "" {
			return rt.ToValue(base)
		}
		if dir == root {
			return rt.ToValue(dir + base)
		}
		return rt.ToValue(dir + "/" + base)
	})
	mustSet(rt, m, "sep", "/")
	mustSet(rt, m, "delimiter", ":")
	mustSet(rt, m, "posix", m)
	return m
}

// --- POSIX path helpers (Node-compatible semantics) ---

func posixRoot(p string) string {
	if strings.HasPrefix(p, "/") {
		return "/"
	}
	return ""
}

func posixBasename(p string) string {
	// Strip trailing slashes.
	for len(p) > 0 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	if p == "" {
		return ""
	}
	i := strings.LastIndex(p, "/")
	if i >= 0 {
		return p[i+1:]
	}
	return p
}

func posixDirname(p string) string {
	if p == "" {
		return "."
	}
	// Strip trailing slashes (but remember absolute).
	isAbs := p[0] == '/'
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "."
	}
	dir := p[:i]
	if dir == "" {
		return "/"
	}
	// Strip trailing slashes from dir.
	for len(dir) > 1 && dir[len(dir)-1] == '/' {
		dir = dir[:len(dir)-1]
	}
	if dir == "" && isAbs {
		return "/"
	}
	return dir
}

// posixExtname implements Node's path.extname algorithm.
func posixExtname(p string) string {
	base := posixBasename(p)
	if base == "" {
		return ""
	}
	startDot := -1
	end := len(base)
	preDotState := 0 // 0=initial, 1=only dots seen, -1=saw non-dot before dot
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '.' {
			if startDot == -1 {
				startDot = i
			} else if preDotState != 1 {
				preDotState = 1
			}
		} else if startDot != -1 {
			preDotState = -1
		}
	}
	if startDot == -1 || preDotState == 0 ||
		(preDotState == 1 && startDot == end-1 && startDot == 1) {
		return ""
	}
	return base[startDot:]
}

func posixNormalize(p string) string {
	if p == "" {
		return "."
	}
	isAbs := p[0] == '/'
	trailingSlash := p[len(p)-1] == '/'
	parts := strings.Split(p, "/")
	var out []string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(out) > 0 && out[len(out)-1] != ".." {
				out = out[:len(out)-1]
			} else if !isAbs {
				out = append(out, "..")
			}
		} else {
			out = append(out, part)
		}
	}
	result := strings.Join(out, "/")
	if isAbs {
		result = "/" + result
	}
	if result == "" {
		return "."
	}
	if trailingSlash && result != "/" {
		result += "/"
	}
	return result
}

func posixJoin(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) == 0 {
		return "."
	}
	return posixNormalize(strings.Join(nonEmpty, "/"))
}

func posixResolve(cwd string, segments ...string) string {
	result := ""
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "/") {
			result = seg
		} else if result == "" {
			result = seg
		} else {
			result = result + "/" + seg
		}
	}
	if !strings.HasPrefix(result, "/") {
		result = cwd + "/" + result
	}
	resolved := posixNormalize(result)
	// resolve never returns trailing slash (unlike normalize)
	if len(resolved) > 1 && resolved[len(resolved)-1] == '/' {
		resolved = resolved[:len(resolved)-1]
	}
	return resolved
}

func posixRelative(cwd, from, to string) string {
	fromAbs := posixResolve(cwd, from)
	toAbs := posixResolve(cwd, to)
	if fromAbs == toAbs {
		return ""
	}
	fromTrimmed := strings.Trim(fromAbs, "/")
	toTrimmed := strings.Trim(toAbs, "/")
	var fromParts, toParts []string
	if fromTrimmed != "" {
		fromParts = strings.Split(fromTrimmed, "/")
	}
	if toTrimmed != "" {
		toParts = strings.Split(toTrimmed, "/")
	}
	common := 0
	for common < len(fromParts) && common < len(toParts) && fromParts[common] == toParts[common] {
		common++
	}
	var out []string
	for i := common; i < len(fromParts); i++ {
		out = append(out, "..")
	}
	out = append(out, toParts[common:]...)
	return strings.Join(out, "/")
}

// posixParseDir returns the dir field for path.parse.
// Unlike dirname, returns "" for bare filenames without directory separators.
func posixParseDir(p string) string {
	stripped := p
	for len(stripped) > 1 && stripped[len(stripped)-1] == '/' {
		stripped = stripped[:len(stripped)-1]
	}
	i := strings.LastIndex(stripped, "/")
	if i < 0 {
		return ""
	}
	if i == 0 {
		return "/"
	}
	return stripped[:i]
}

func stringProp(obj *sobek.Object, name string) string {
	v := obj.Get(name)
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return ""
	}
	return v.String()
}

// --- fs module ---

func (h *shimHost) newFSModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	set := func(name string, fn func(sobek.FunctionCall) sobek.Value) {
		if err := m.Set(name, fn); err != nil {
			panic(rt.NewTypeError(err.Error()))
		}
	}

	set("existsSync", func(call sobek.FunctionCall) sobek.Value {
		_, err := os.Stat(h.resolvePath(call.Argument(0).String()))
		return rt.ToValue(err == nil)
	})

	set("accessSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		info, err := os.Stat(p)
		if err == nil && len(call.Arguments) > 1 {
			mode := int(call.Argument(1).ToInteger())
			permissions := info.Mode().Perm()
			if mode&4 != 0 && permissions&0o444 == 0 || mode&2 != 0 && permissions&0o222 == 0 || mode&1 != 0 && permissions&0o111 == 0 {
				err = os.ErrPermission
			}
		}
		if err != nil {
			panicFS(rt, err, "access", p)
		}
		return sobek.Undefined()
	})

	set("realpathSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			panicFS(rt, err, "realpath", p)
		}
		return rt.ToValue(resolved)
	})

	set("readFileSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		data, err := os.ReadFile(p)
		if err != nil {
			panicFS(rt, err, "open", p)
		}
		if hasEncoding(rt, call, 1) {
			return rt.ToValue(string(data))
		}
		return newBufferValue(rt, data)
	})

	set("writeFileSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		content := exportBytes(rt, call.Argument(1))
		if err := os.WriteFile(p, content, 0o644); err != nil {
			panicFS(rt, err, "open", p)
		}
		return sobek.Undefined()
	})

	set("appendFileSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		content := exportBytes(rt, call.Argument(1))
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			panicFS(rt, err, "open", p)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write(content); err != nil {
			panicFS(rt, err, "write", p)
		}
		return sobek.Undefined()
	})

	set("createWriteStream", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		flags := "w"
		mode := os.FileMode(0o666)
		if options := call.Argument(1); present(options) {
			optionsObject := options.ToObject(rt)
			if value := optionsObject.Get("flags"); present(value) {
				flags = value.String()
			}
			if value := optionsObject.Get("mode"); present(value) {
				mode = os.FileMode(value.ToInteger())
			}
		}
		openFlags, ok := map[string]int{
			"a": os.O_APPEND | os.O_CREATE | os.O_WRONLY, "a+": os.O_APPEND | os.O_CREATE | os.O_RDWR,
			"ax": os.O_APPEND | os.O_CREATE | os.O_EXCL | os.O_WRONLY, "ax+": os.O_APPEND | os.O_CREATE | os.O_EXCL | os.O_RDWR,
			"w": os.O_CREATE | os.O_TRUNC | os.O_WRONLY, "w+": os.O_CREATE | os.O_TRUNC | os.O_RDWR,
			"wx": os.O_CREATE | os.O_EXCL | os.O_WRONLY, "wx+": os.O_CREATE | os.O_EXCL | os.O_RDWR,
		}[flags]
		if !ok {
			panic(rt.NewTypeError("unsupported createWriteStream flag %q", flags))
		}
		file, err := os.OpenFile(p, openFlags, mode)
		if err != nil {
			panicFS(rt, err, "open", p)
		}

		stream := rt.NewObject()
		closed := false
		bytesWritten := int64(0)
		mustSet(rt, stream, "path", p)
		mustSet(rt, stream, "bytesWritten", bytesWritten)
		mustSet(rt, stream, "closed", closed)
		writeChunk := func(value sobek.Value) {
			if closed {
				panic(rt.NewTypeError("write after end"))
			}
			written, writeErr := file.Write(exportBytes(rt, value))
			if writeErr != nil {
				panicFS(rt, writeErr, "write", p)
			}
			bytesWritten += int64(written)
			mustSet(rt, stream, "bytesWritten", bytesWritten)
		}
		postCallback := func(value sobek.Value) {
			callback, callable := sobek.AssertFunction(value)
			if !callable {
				return
			}
			h.vm.post(h.vm.context(), true, func(_ *sobek.Runtime) error {
				_, callbackErr := callback(sobek.Undefined())
				return callbackErr
			})
		}
		mustSet(rt, stream, "write", func(call sobek.FunctionCall) sobek.Value {
			writeChunk(call.Argument(0))
			if len(call.Arguments) > 1 {
				postCallback(call.Arguments[len(call.Arguments)-1])
			}
			return rt.ToValue(true)
		})
		mustSet(rt, stream, "end", func(call sobek.FunctionCall) sobek.Value {
			if closed {
				return stream
			}
			if len(call.Arguments) > 0 {
				if _, isCallback := sobek.AssertFunction(call.Argument(0)); !isCallback && present(call.Argument(0)) {
					writeChunk(call.Argument(0))
				}
			}
			if closeErr := file.Close(); closeErr != nil {
				panicFS(rt, closeErr, "close", p)
			}
			closed = true
			mustSet(rt, stream, "closed", closed)
			if len(call.Arguments) > 0 {
				postCallback(call.Arguments[len(call.Arguments)-1])
			}
			return stream
		})
		return stream
	})

	set("readdirSync", func(call sobek.FunctionCall) sobek.Value {
		dir := h.resolvePath(call.Argument(0).String())
		withFileTypes := fsBooleanOption(rt, call.Argument(1), "withFileTypes")
		entries, err := os.ReadDir(dir)
		if err != nil {
			panicFS(rt, err, "scandir", dir)
		}
		if !withFileTypes {
			names := make([]any, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			return rt.ToValue(names)
		}
		dirents := make([]any, len(entries))
		for i, e := range entries {
			dirents[i] = newDirentObject(rt, e)
		}
		return rt.ToValue(dirents)
	})

	set("statSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		info, err := os.Stat(p)
		if err != nil {
			panicFS(rt, err, "stat", p)
		}
		return newStatObject(rt, info)
	})

	set("lstatSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		info, err := os.Lstat(p)
		if err != nil {
			panicFS(rt, err, "lstat", p)
		}
		return newStatObject(rt, info)
	})

	set("mkdirSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		recursive := false
		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Argument(1)) {
			opts := call.Argument(1).ToObject(rt)
			r := opts.Get("recursive")
			if r != nil && !sobek.IsUndefined(r) {
				recursive = r.ToBoolean()
			}
		}
		var err error
		if recursive {
			err = os.MkdirAll(p, 0o755)
		} else {
			err = os.Mkdir(p, 0o755)
		}
		if err != nil {
			panicFS(rt, err, "mkdir", p)
		}
		return sobek.Undefined()
	})

	set("mkdtempSync", func(call sobek.FunctionCall) sobek.Value {
		prefix := call.Argument(0).String()
		if !filepath.IsAbs(prefix) {
			prefix = filepath.Join(h.cwd, prefix)
		}
		dir, err := os.MkdirTemp(filepath.Dir(prefix), filepath.Base(prefix))
		if err != nil {
			panicFS(rt, err, "mkdtemp", prefix)
		}
		return rt.ToValue(dir)
	})

	set("chmodSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		if err := os.Chmod(p, os.FileMode(call.Argument(1).ToInteger())); err != nil {
			panicFS(rt, err, "chmod", p)
		}
		return sobek.Undefined()
	})

	set("readlinkSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		target, err := os.Readlink(p)
		if err != nil {
			panicFS(rt, err, "readlink", p)
		}
		return rt.ToValue(target)
	})

	set("unlinkSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		if err := os.Remove(p); err != nil {
			panicFS(rt, err, "unlink", p)
		}
		return sobek.Undefined()
	})

	set("rmdirSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		if err := os.Remove(p); err != nil {
			panicFS(rt, err, "rmdir", p)
		}
		return sobek.Undefined()
	})

	set("rmSync", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		recursive, force := fsRemoveOptions(rt, call.Argument(1))
		if err := removeFSPath(p, recursive, force); err != nil {
			panicFS(rt, err, "rm", p)
		}
		return sobek.Undefined()
	})

	set("copyFileSync", func(call sobek.FunctionCall) sobek.Value {
		src := h.resolvePath(call.Argument(0).String())
		dst := h.resolvePath(call.Argument(1).String())
		if err := copyFilePath(src, dst); err != nil {
			panicFS(rt, err, "copyfile", dst)
		}
		return sobek.Undefined()
	})

	set("cpSync", func(call sobek.FunctionCall) sobek.Value {
		src := h.resolvePath(call.Argument(0).String())
		dst := h.resolvePath(call.Argument(1).String())
		recursive := fsBooleanOption(rt, call.Argument(2), "recursive")
		if err := copyFSPath(src, dst, recursive); err != nil {
			panicFS(rt, err, "cp", src)
		}
		return sobek.Undefined()
	})

	set("renameSync", func(call sobek.FunctionCall) sobek.Value {
		oldPath := h.resolvePath(call.Argument(0).String())
		newPath := h.resolvePath(call.Argument(1).String())
		if err := os.Rename(oldPath, newPath); err != nil {
			panicFS(rt, err, "rename", oldPath)
		}
		return sobek.Undefined()
	})

	set("watch", func(call sobek.FunctionCall) sobek.Value {
		target := h.resolvePath(call.Argument(0).String())
		var listener sobek.Callable
		for i := 1; i < len(call.Arguments); i++ {
			if fn, ok := sobek.AssertFunction(call.Argument(i)); ok {
				listener = fn
				break
			}
		}

		// Baseline stat taken synchronously so writes after watch() are detected.
		var lastMod time.Time
		var lastSize int64
		if info, err := os.Stat(target); err == nil {
			lastMod = info.ModTime()
			lastSize = info.Size()
		}

		id := h.vm.allocID()
		ctx, cancel := context.WithCancel(h.vm.rootCtx)
		h.vm.mu.Lock()
		h.vm.ops[id] = cancel
		h.vm.mu.Unlock()

		if listener != nil {
			h.vm.wg.Add(1)
			go func() {
				defer h.vm.wg.Done()
				// ponytail: 100ms poll — upgrade to inotify/kqueue if throughput matters
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						info, err := os.Stat(target)
						if err != nil {
							continue
						}
						if info.ModTime() != lastMod || info.Size() != lastSize {
							lastMod = info.ModTime()
							lastSize = info.Size()
							h.vm.postCallback(func(rt *sobek.Runtime) {
								select {
								case <-ctx.Done():
									return
								default:
								}
								_, _ = listener(sobek.Undefined(), rt.ToValue("change"), rt.ToValue(filepath.Base(target)))
							})
						}
					case <-ctx.Done():
						return
					}
				}
			}()
		}

		watcher := rt.NewObject()
		mustSet(rt, watcher, "close", func(sobek.FunctionCall) sobek.Value {
			h.vm.cancelOp(id)
			return sobek.Undefined()
		})
		mustSet(rt, watcher, "on", func(c sobek.FunctionCall) sobek.Value { return watcher })
		mustSet(rt, watcher, "unref", func(sobek.FunctionCall) sobek.Value { return watcher })
		mustSet(rt, watcher, "ref", func(sobek.FunctionCall) sobek.Value { return watcher })
		return watcher
	})

	set("createReadStream", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		data, err := os.ReadFile(p)
		if err != nil {
			panicFS(rt, err, "open", p)
		}
		stream := rt.NewObject()
		mustSet(rt, stream, "on", func(c sobek.FunctionCall) sobek.Value {
			event := c.Argument(0).String()
			handler, ok := sobek.AssertFunction(c.Argument(1))
			if !ok {
				return stream
			}
			switch event {
			case "data":
				_, _ = handler(sobek.Undefined(), newBufferValue(rt, data))
			case "end":
				_, _ = handler(sobek.Undefined())
			}
			return stream
		})
		mustSet(rt, stream, "pipe", func(c sobek.FunctionCall) sobek.Value { return c.Argument(0) })
		return stream
	})

	// fs.constants
	constants := rt.NewObject()
	mustSet(rt, constants, "F_OK", 0)
	mustSet(rt, constants, "R_OK", 4)
	mustSet(rt, constants, "W_OK", 2)
	mustSet(rt, constants, "X_OK", 1)
	mustSet(rt, m, "constants", constants)

	// fs.promises submodule
	mustSet(rt, m, "promises", h.newFSPromisesModule(rt))

	return m
}

func (h *shimHost) newFSPromisesModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	set := func(name string, fn func(sobek.FunctionCall) sobek.Value) {
		if err := m.Set(name, fn); err != nil {
			panic(rt.NewTypeError(err.Error()))
		}
	}

	set("readFile", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		data, err := os.ReadFile(p)
		if err != nil {
			return rejectFS(rt, err, "open", p)
		}
		if hasEncoding(rt, call, 1) {
			return resolvePromise(rt, rt.ToValue(string(data)))
		}
		return resolvePromise(rt, newBufferValue(rt, data))
	})

	set("open", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		flags := "r"
		if present(call.Argument(1)) {
			flags = call.Argument(1).String()
		}
		openFlags, ok := nodeOpenFlags(flags)
		if !ok {
			return rejectPromise(rt, fmt.Errorf("unsupported open flag %q", flags))
		}
		mode := os.FileMode(0o666)
		if present(call.Argument(2)) {
			mode = os.FileMode(call.Argument(2).ToInteger())
		}
		file, err := os.OpenFile(p, openFlags, mode)
		if err != nil {
			return rejectFS(rt, err, "open", p)
		}
		return resolvePromise(rt, newFileHandleObject(rt, file, p))
	})

	set("writeFile", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		content := exportBytes(rt, call.Argument(1))
		if err := os.WriteFile(p, content, 0o644); err != nil {
			return rejectFS(rt, err, "open", p)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("stat", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		info, err := os.Stat(p)
		if err != nil {
			return rejectFS(rt, err, "stat", p)
		}
		return resolvePromise(rt, newStatObject(rt, info))
	})

	set("lstat", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		info, err := os.Lstat(p)
		if err != nil {
			return rejectFS(rt, err, "lstat", p)
		}
		return resolvePromise(rt, newStatObject(rt, info))
	})

	set("realpath", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			return rejectFS(rt, err, "realpath", p)
		}
		return resolvePromise(rt, rt.ToValue(resolved))
	})

	set("mkdir", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		recursive := false
		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Argument(1)) {
			opts := call.Argument(1).ToObject(rt)
			r := opts.Get("recursive")
			if r != nil && !sobek.IsUndefined(r) {
				recursive = r.ToBoolean()
			}
		}
		var err error
		if recursive {
			err = os.MkdirAll(p, 0o755)
		} else {
			err = os.Mkdir(p, 0o755)
		}
		if err != nil {
			return rejectFS(rt, err, "mkdir", p)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("access", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		_, err := os.Stat(p)
		if err != nil {
			return rejectFS(rt, err, "access", p)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("appendFile", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		content := exportBytes(rt, call.Argument(1))
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return rejectFS(rt, err, "open", p)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write(content); err != nil {
			return rejectFS(rt, err, "write", p)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("mkdtemp", func(call sobek.FunctionCall) sobek.Value {
		prefix := call.Argument(0).String()
		if !filepath.IsAbs(prefix) {
			prefix = filepath.Join(h.cwd, prefix)
		}
		dir, err := os.MkdirTemp(filepath.Dir(prefix), filepath.Base(prefix))
		if err != nil {
			return rejectFS(rt, err, "mkdtemp", prefix)
		}
		return resolvePromise(rt, rt.ToValue(dir))
	})

	set("unlink", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		if err := os.Remove(p); err != nil {
			return rejectFS(rt, err, "unlink", p)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("rename", func(call sobek.FunctionCall) sobek.Value {
		oldPath := h.resolvePath(call.Argument(0).String())
		newPath := h.resolvePath(call.Argument(1).String())
		if err := os.Rename(oldPath, newPath); err != nil {
			return rejectFS(rt, err, "rename", oldPath)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("chmod", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		if err := os.Chmod(p, os.FileMode(call.Argument(1).ToInteger())); err != nil {
			return rejectFS(rt, err, "chmod", p)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("readlink", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		target, err := os.Readlink(p)
		if err != nil {
			return rejectFS(rt, err, "readlink", p)
		}
		return resolvePromise(rt, rt.ToValue(target))
	})

	set("rm", func(call sobek.FunctionCall) sobek.Value {
		p := h.resolvePath(call.Argument(0).String())
		recursive, force := fsRemoveOptions(rt, call.Argument(1))
		if err := removeFSPath(p, recursive, force); err != nil {
			return rejectFS(rt, err, "rm", p)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("copyFile", func(call sobek.FunctionCall) sobek.Value {
		src := h.resolvePath(call.Argument(0).String())
		dst := h.resolvePath(call.Argument(1).String())
		if err := copyFilePath(src, dst); err != nil {
			return rejectFS(rt, err, "copyfile", dst)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	set("readdir", func(call sobek.FunctionCall) sobek.Value {
		dir := h.resolvePath(call.Argument(0).String())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return rejectFS(rt, err, "scandir", dir)
		}
		values := make([]any, len(entries))
		withFileTypes := fsBooleanOption(rt, call.Argument(1), "withFileTypes")
		for i, entry := range entries {
			if withFileTypes {
				values[i] = newDirentObject(rt, entry)
			} else {
				values[i] = entry.Name()
			}
		}
		return resolvePromise(rt, rt.ToValue(values))
	})

	set("cp", func(call sobek.FunctionCall) sobek.Value {
		src := h.resolvePath(call.Argument(0).String())
		dst := h.resolvePath(call.Argument(1).String())
		recursive := fsBooleanOption(rt, call.Argument(2), "recursive")
		if err := copyFSPath(src, dst, recursive); err != nil {
			return rejectFS(rt, err, "cp", src)
		}
		return resolvePromise(rt, sobek.Undefined())
	})

	return m
}

func nodeOpenFlags(flag string) (int, bool) {
	flags, ok := map[string]int{
		"r": os.O_RDONLY, "r+": os.O_RDWR,
		"rs": os.O_RDONLY | os.O_SYNC, "rs+": os.O_RDWR | os.O_SYNC,
		"a": os.O_APPEND | os.O_CREATE | os.O_WRONLY, "a+": os.O_APPEND | os.O_CREATE | os.O_RDWR,
		"ax": os.O_APPEND | os.O_CREATE | os.O_EXCL | os.O_WRONLY, "ax+": os.O_APPEND | os.O_CREATE | os.O_EXCL | os.O_RDWR,
		"w": os.O_CREATE | os.O_TRUNC | os.O_WRONLY, "w+": os.O_CREATE | os.O_TRUNC | os.O_RDWR,
		"wx": os.O_CREATE | os.O_EXCL | os.O_WRONLY, "wx+": os.O_CREATE | os.O_EXCL | os.O_RDWR,
	}[flag]
	return flags, ok
}

func newDirentObject(rt *sobek.Runtime, entry os.DirEntry) *sobek.Object {
	dirent := rt.NewObject()
	isDirectory := entry.IsDir()
	isFile := entry.Type().IsRegular()
	isSymbolicLink := entry.Type()&os.ModeSymlink != 0
	mustSet(rt, dirent, "name", entry.Name())
	mustSet(rt, dirent, "isDirectory", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(isDirectory) })
	mustSet(rt, dirent, "isFile", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(isFile) })
	mustSet(rt, dirent, "isSymbolicLink", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(isSymbolicLink) })
	return dirent
}

func newFileHandleObject(rt *sobek.Runtime, file *os.File, path string) *sobek.Object {
	handle := rt.NewObject()
	mustSet(rt, handle, "fd", int64(file.Fd()))
	mustSet(rt, handle, "stat", func(sobek.FunctionCall) sobek.Value {
		info, err := file.Stat()
		if err != nil {
			return rejectFS(rt, err, "fstat", path)
		}
		return resolvePromise(rt, newStatObject(rt, info))
	})
	mustSet(rt, handle, "read", func(call sobek.FunctionCall) sobek.Value {
		buffer := call.Argument(0)
		data := exportBytes(rt, buffer)
		offset := int(call.Argument(1).ToInteger())
		length := len(data) - offset
		if present(call.Argument(2)) {
			length = int(call.Argument(2).ToInteger())
		}
		if offset < 0 || length < 0 || offset > len(data) || length > len(data)-offset {
			return rejectPromise(rt, fmt.Errorf("offset or length is out of range"))
		}
		var (
			bytesRead int
			err       error
		)
		position := call.Argument(3)
		if present(position) {
			bytesRead, err = file.ReadAt(data[offset:offset+length], position.ToInteger())
		} else {
			bytesRead, err = file.Read(data[offset : offset+length])
		}
		if err != nil && err != io.EOF {
			return rejectFS(rt, err, "read", path)
		}
		result := rt.NewObject()
		mustSet(rt, result, "bytesRead", bytesRead)
		mustSet(rt, result, "buffer", buffer)
		return resolvePromise(rt, result)
	})
	mustSet(rt, handle, "close", func(sobek.FunctionCall) sobek.Value {
		if err := file.Close(); err != nil {
			return rejectFS(rt, err, "close", path)
		}
		return resolvePromise(rt, sobek.Undefined())
	})
	return handle
}

func fsBooleanOption(rt *sobek.Runtime, value sobek.Value, name string) bool {
	if !present(value) {
		return false
	}
	option := value.ToObject(rt).Get(name)
	return present(option) && option.ToBoolean()
}

func fsRemoveOptions(rt *sobek.Runtime, value sobek.Value) (recursive, force bool) {
	return fsBooleanOption(rt, value, "recursive"), fsBooleanOption(rt, value, "force")
}

func removeFSPath(path string, recursive, force bool) error {
	if _, err := os.Lstat(path); err != nil {
		if force && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if recursive {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func copyFilePath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err = io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func copyFSPath(src, dst string, recursive bool) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if !recursive {
			return fmt.Errorf("recursive option is required to copy directory %s", src)
		}
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyFSPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), true); err != nil {
				return err
			}
		}
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	return copyFilePath(src, dst)
}

func newStatObject(rt *sobek.Runtime, info os.FileInfo) *sobek.Object {
	s := rt.NewObject()
	mt := info.ModTime()
	mtimeMs := float64(mt.UnixMilli())
	mustSet(rt, s, "size", info.Size())
	mustSet(rt, s, "mode", int(info.Mode()))
	mustSet(rt, s, "mtimeMs", mtimeMs)
	mustSet(rt, s, "atimeMs", mtimeMs)
	mustSet(rt, s, "ctimeMs", mtimeMs)
	mustSet(rt, s, "birthtimeMs", mtimeMs)
	mtime := rt.NewObject()
	mustSet(rt, mtime, "getTime", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(mtimeMs) })
	mustSet(rt, mtime, "toISOString", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(mt.UTC().Format(time.RFC3339Nano)) })
	mustSet(rt, s, "mtime", mtime)
	mustSet(rt, s, "atime", mtime)
	mustSet(rt, s, "ctime", mtime)
	mustSet(rt, s, "birthtime", mtime)
	mustSet(rt, s, "isFile", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(info.Mode().IsRegular()) })
	mustSet(rt, s, "isDirectory", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(info.IsDir()) })
	mustSet(rt, s, "isSymbolicLink", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(info.Mode()&os.ModeSymlink != 0) })
	return s
}

// --- os module ---

func newOSModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	mustSet(rt, m, "homedir", func(sobek.FunctionCall) sobek.Value {
		home, _ := os.UserHomeDir()
		return rt.ToValue(home)
	})
	mustSet(rt, m, "tmpdir", func(sobek.FunctionCall) sobek.Value {
		return rt.ToValue(os.TempDir())
	})
	mustSet(rt, m, "hostname", func(sobek.FunctionCall) sobek.Value {
		h, _ := os.Hostname()
		return rt.ToValue(h)
	})
	mustSet(rt, m, "platform", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(runtime.GOOS) })
	mustSet(rt, m, "arch", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(nodeArch()) })
	mustSet(rt, m, "type", func(sobek.FunctionCall) sobek.Value {
		if runtime.GOOS == "darwin" {
			return rt.ToValue("Darwin")
		}
		return rt.ToValue("Linux")
	})
	mustSet(rt, m, "EOL", "\n")
	mustSet(rt, m, "cpus", func(sobek.FunctionCall) sobek.Value {
		return rt.ToValue(make([]any, runtime.NumCPU()))
	})
	mustSet(rt, m, "totalmem", func(sobek.FunctionCall) sobek.Value {
		return rt.ToValue(float64(nodeTotalMemory()))
	})
	return m
}

var (
	totalMemoryOnce  sync.Once
	totalMemoryBytes uint64
)

func nodeTotalMemory() uint64 {
	totalMemoryOnce.Do(func() {
		switch runtime.GOOS {
		case "linux":
			contents, err := os.ReadFile("/proc/meminfo")
			if err != nil {
				return
			}
			fields := strings.Fields(string(contents))
			if len(fields) < 3 || fields[0] != "MemTotal:" {
				return
			}
			kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil {
				totalMemoryBytes = kilobytes * 1024
			}
		case "darwin":
			contents, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
			if err != nil {
				return
			}
			totalMemoryBytes, _ = strconv.ParseUint(strings.TrimSpace(string(contents)), 10, 64)
		}
	})
	return totalMemoryBytes
}

// --- process global ---

func (h *shimHost) installProcess(rt *sobek.Runtime) error {
	proc := rt.NewObject()
	populateEventEmitter(rt, proc)
	mustSet(rt, proc, "cwd", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(h.cwd) })
	mustSet(rt, proc, "platform", runtime.GOOS)
	mustSet(rt, proc, "arch", nodeArch())
	mustSet(rt, proc, "pid", os.Getpid())
	if uid, ok := processUID(); ok {
		mustSet(rt, proc, "getuid", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(uid) })
	}
	mustSet(rt, proc, "argv", []string{"pigo", "extension"})
	mustSet(rt, proc, "execPath", func() string {
		if p, err := os.Executable(); err == nil {
			return p
		}
		return "pigo"
	}())
	mustSet(rt, proc, "exit", func(call sobek.FunctionCall) sobek.Value {
		// Extensions should not kill the host process; throw instead
		code := 0
		if len(call.Arguments) > 0 {
			code = int(call.Argument(0).ToInteger())
		}
		panic(rt.NewTypeError("process.exit(%d) is not supported in extensions", code))
	})
	mustSet(rt, proc, "kill", func(sobek.FunctionCall) sobek.Value { return sobek.Undefined() })

	// process.env — snapshot of current env
	env := rt.NewObject()
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			mustSet(rt, env, k, v)
		}
	}
	mustSet(rt, proc, "env", env)

	// process.stdout.write
	stdout := rt.NewObject()
	stdoutTTY := terminalFD(1)
	mustSet(rt, stdout, "fd", 1)
	mustSet(rt, stdout, "isTTY", stdoutTTY)
	mustSet(rt, stdout, "write", func(call sobek.FunctionCall) sobek.Value {
		_, _ = fmt.Fprint(os.Stdout, call.Argument(0).String())
		return rt.ToValue(true)
	})
	mustSet(rt, proc, "stdout", stdout)
	stderr := rt.NewObject()
	stderrTTY := terminalFD(2)
	mustSet(rt, stderr, "fd", 2)
	mustSet(rt, stderr, "isTTY", stderrTTY)
	mustSet(rt, stderr, "write", func(call sobek.FunctionCall) sobek.Value {
		_, _ = fmt.Fprint(os.Stderr, call.Argument(0).String())
		return rt.ToValue(true)
	})
	mustSet(rt, proc, "stderr", stderr)

	mustSet(rt, proc, "version", "v22.0.0")
	mustSet(rt, proc, "versions", rt.NewObject())

	return rt.Set("process", proc)
}

// --- url module ---

func newURLModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	mustSet(rt, m, "fileURLToPath", func(call sobek.FunctionCall) sobek.Value {
		u := call.Argument(0).String()
		if strings.HasPrefix(u, "file://") {
			if parsed, err := url.Parse(u); err == nil && parsed.Path != "" {
				return rt.ToValue(parsed.Path)
			}
			return rt.ToValue(strings.TrimPrefix(u, "file://"))
		}
		return rt.ToValue(u)
	})
	mustSet(rt, m, "pathToFileURL", func(call sobek.FunctionCall) sobek.Value {
		p := call.Argument(0).String()
		obj := rt.NewObject()
		mustSet(rt, obj, "href", "file://"+p)
		mustSet(rt, obj, "pathname", p)
		mustSet(rt, obj, "toString", func(sobek.FunctionCall) sobek.Value { return rt.ToValue("file://" + p) })
		return obj
	})
	// Keep node:url and the global constructor on the same ResolveReference path.
	mustSet(rt, m, "URL", rt.Get("URL"))
	return m
}

// --- util module ---

func newUtilModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	mustSet(rt, m, "deprecate", func(call sobek.FunctionCall) sobek.Value {
		fn, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			panic(rt.NewTypeError("The \"fn\" argument must be of type function"))
		}
		message := call.Argument(1).String()
		code := ""
		if value := call.Argument(2); present(value) {
			code = value.String()
		}
		warned := false
		return rt.ToValue(func(inner sobek.FunctionCall) sobek.Value {
			if !warned {
				warned = true
				if code == "" {
					_, _ = fmt.Fprintf(os.Stderr, "DeprecationWarning: %s\n", message)
				} else {
					_, _ = fmt.Fprintf(os.Stderr, "[%s] DeprecationWarning: %s\n", code, message)
				}
			}
			value, err := fn(inner.This, inner.Arguments...)
			if err != nil {
				panic(err)
			}
			return value
		})
	})
	mustSet(rt, m, "promisify", func(call sobek.FunctionCall) sobek.Value {
		fn, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			panic(rt.NewTypeError("util.promisify: argument is not a function"))
		}
		wrapped := func(inner sobek.FunctionCall) sobek.Value {
			promise, resolve, reject := rt.NewPromise()
			args := make([]sobek.Value, 0, len(inner.Arguments)+1)
			args = append(args, inner.Arguments...)
			args = append(args, rt.ToValue(func(errCall sobek.FunctionCall) sobek.Value {
				errArg := errCall.Argument(0)
				if errArg != nil && !sobek.IsUndefined(errArg) && !sobek.IsNull(errArg) {
					_ = reject(errArg)
				} else if len(errCall.Arguments) > 1 {
					_ = resolve(errCall.Argument(1))
				} else {
					_ = resolve(sobek.Undefined())
				}
				return sobek.Undefined()
			}))
			_, _ = fn(inner.This, args...)
			return rt.ToValue(promise)
		}
		return rt.ToValue(wrapped)
	})
	mustSet(rt, m, "inspect", func(call sobek.FunctionCall) sobek.Value {
		v := call.Argument(0)
		if v == nil || sobek.IsUndefined(v) {
			return rt.ToValue("undefined")
		}
		if sobek.IsNull(v) {
			return rt.ToValue("null")
		}
		data, err := json.Marshal(v.Export())
		if err != nil {
			return rt.ToValue(v.String())
		}
		return rt.ToValue(string(data))
	})
	mustSet(rt, m, "format", func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) == 0 {
			return rt.ToValue("")
		}
		result := call.Argument(0).String()
		for i := 1; i < len(call.Arguments); i++ {
			if idx := strings.Index(result, "%s"); idx >= 0 {
				result = result[:idx] + call.Argument(i).String() + result[idx+2:]
			} else if idx := strings.Index(result, "%d"); idx >= 0 {
				result = result[:idx] + call.Argument(i).String() + result[idx+2:]
			} else if idx := strings.Index(result, "%j"); idx >= 0 {
				data, _ := json.Marshal(call.Argument(i).Export())
				result = result[:idx] + string(data) + result[idx+2:]
			}
		}
		return rt.ToValue(result)
	})
	return m
}

// --- child_process module ---

func (h *shimHost) newChildProcessModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	set := func(name string, fn func(sobek.FunctionCall) sobek.Value) {
		if err := m.Set(name, fn); err != nil {
			panic(rt.NewTypeError(err.Error()))
		}
	}

	set("execSync", func(call sobek.FunctionCall) sobek.Value {
		command := call.Argument(0).String()
		opts := h.parseExecOptions(rt, call, 1)
		result, _ := extensions.Exec(h.vm.rootCtx, "sh", []string{"-c", command}, opts)
		if result.Code != 0 {
			// Node attaches the exit code as err.status (and .code) on the
			// thrown execSync error; extensions branch on it.
			failure := rt.NewTypeError("Command failed: %s (exit %d)", command, result.Code)
			mustSet(rt, failure, "status", result.Code)
			mustSet(rt, failure, "code", result.Code)
			mustSet(rt, failure, "stdout", result.Stdout)
			mustSet(rt, failure, "stderr", result.Stderr)
			panic(failure)
		}
		return rt.ToValue(result.Stdout)
	})

	set("execFileSync", func(call sobek.FunctionCall) sobek.Value {
		file := call.Argument(0).String()
		var args []string
		optionsIndex := 1
		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Argument(1)) && !sobek.IsNull(call.Argument(1)) {
			if exported, ok := call.Argument(1).Export().([]any); ok {
				for _, argument := range exported {
					args = append(args, fmt.Sprint(argument))
				}
				optionsIndex = 2
			}
		}
		opts := h.parseExecOptions(rt, call, optionsIndex)
		result, _ := extensions.Exec(h.vm.rootCtx, file, args, opts)
		if result.Killed || result.Code != 0 {
			failure := rt.NewTypeError("Command failed: %s", file)
			mustSet(rt, failure, "status", result.Code)
			mustSet(rt, failure, "code", result.Code)
			mustSet(rt, failure, "stdout", result.Stdout)
			mustSet(rt, failure, "stderr", result.Stderr)
			mustSet(rt, failure, "killed", result.Killed)
			panic(failure)
		}
		if hasEncoding(rt, call, optionsIndex) {
			return rt.ToValue(result.Stdout)
		}
		return newBufferValue(rt, []byte(result.Stdout))
	})

	set("exec", func(call sobek.FunctionCall) sobek.Value {
		command := call.Argument(0).String()
		opts := h.parseExecOptions(rt, call, 1)
		var callback sobek.Callable
		for i := len(call.Arguments) - 1; i >= 1; i-- {
			if fn, ok := sobek.AssertFunction(call.Argument(i)); ok {
				callback = fn
				break
			}
		}
		cp := rt.NewObject()
		mustSet(rt, cp, "on", func(sobek.FunctionCall) sobek.Value { return cp })
		mustSet(rt, cp, "stdout", rt.NewObject())
		mustSet(rt, cp, "stderr", rt.NewObject())
		h.vm.wg.Add(1)
		go func() {
			defer h.vm.wg.Done()
			result, _ := extensions.Exec(h.vm.rootCtx, "sh", []string{"-c", command}, opts)
			if callback != nil {
				h.vm.postCallback(func(rt *sobek.Runtime) {
					var errVal sobek.Value
					if result.Code != 0 {
						e := rt.NewObject()
						mustSet(rt, e, "code", result.Code)
						mustSet(rt, e, "message", fmt.Sprintf("Command failed: %s", command))
						errVal = e
					} else {
						errVal = sobek.Null()
					}
					_, _ = callback(sobek.Undefined(), errVal, rt.ToValue(result.Stdout), rt.ToValue(result.Stderr))
				})
			}
		}()
		return cp
	})

	set("execFile", func(call sobek.FunctionCall) sobek.Value {
		file := call.Argument(0).String()
		var args []string
		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Argument(1)) {
			if exported, ok := call.Argument(1).Export().([]any); ok {
				for _, a := range exported {
					args = append(args, fmt.Sprint(a))
				}
			}
		}
		opts := h.parseExecOptions(rt, call, 2)
		var callback sobek.Callable
		for i := len(call.Arguments) - 1; i >= 2; i-- {
			if fn, ok := sobek.AssertFunction(call.Argument(i)); ok {
				callback = fn
				break
			}
		}
		cp := rt.NewObject()
		mustSet(rt, cp, "on", func(sobek.FunctionCall) sobek.Value { return cp })
		h.vm.wg.Add(1)
		go func() {
			defer h.vm.wg.Done()
			result, _ := extensions.Exec(h.vm.rootCtx, file, args, opts)
			if callback != nil {
				h.vm.postCallback(func(rt *sobek.Runtime) {
					var errVal sobek.Value
					if result.Code != 0 {
						e := rt.NewObject()
						mustSet(rt, e, "code", result.Code)
						errVal = e
					} else {
						errVal = sobek.Null()
					}
					_, _ = callback(sobek.Undefined(), errVal, rt.ToValue(result.Stdout), rt.ToValue(result.Stderr))
				})
			}
		}()
		return cp
	})

	set("spawnSync", func(call sobek.FunctionCall) sobek.Value {
		command := call.Argument(0).String()
		var args []string
		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Argument(1)) {
			if exported, ok := call.Argument(1).Export().([]any); ok {
				for _, a := range exported {
					args = append(args, fmt.Sprint(a))
				}
			}
		}
		opts := h.parseExecOptions(rt, call, 2)
		result, _ := extensions.Exec(h.vm.rootCtx, command, args, opts)
		o := rt.NewObject()
		mustSet(rt, o, "stdout", result.Stdout)
		mustSet(rt, o, "stderr", result.Stderr)
		mustSet(rt, o, "status", result.Code)
		mustSet(rt, o, "pid", 0)
		return o
	})

	set("spawn", func(call sobek.FunctionCall) sobek.Value {
		command := call.Argument(0).String()
		var args []string
		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Argument(1)) {
			if exported, ok := call.Argument(1).Export().([]any); ok {
				for _, a := range exported {
					args = append(args, fmt.Sprint(a))
				}
			}
		}
		opts := h.parseExecOptions(rt, call, 2)

		// EventEmitter surface (real once/off semantics) on the child and its
		// streams; the detached flow does proc.once("spawn"/"error") + unref().
		cp := rt.NewObject()
		populateEventEmitter(rt, cp)
		stdout := rt.NewObject()
		populateEventEmitter(rt, stdout)
		stderr := rt.NewObject()
		populateEventEmitter(rt, stderr)
		mustSet(rt, cp, "stdout", stdout)
		mustSet(rt, cp, "stderr", stderr)
		mustSet(rt, cp, "stdin", rt.NewObject())
		mustSet(rt, cp, "pid", 0)
		mustSet(rt, cp, "kill", func(sobek.FunctionCall) sobek.Value { return rt.ToValue(true) })
		mustSet(rt, cp, "unref", func(sobek.FunctionCall) sobek.Value { return cp })

		// Defer execution so .on/.once handlers are registered before events fire.
		h.vm.wg.Add(1)
		go func() {
			defer h.vm.wg.Done()
			if _, err := exec.LookPath(command); err != nil {
				h.vm.postCallback(func(rt *sobek.Runtime) {
					emitEvent(rt, cp, "error", nodeFSErrorValue(rt, os.ErrNotExist, "spawn", command))
				})
				return
			}
			h.vm.postCallback(func(rt *sobek.Runtime) { emitEvent(rt, cp, "spawn") })
			result, _ := extensions.Exec(h.vm.rootCtx, command, args, opts)
			h.vm.postCallback(func(rt *sobek.Runtime) {
				if result.Stdout != "" {
					emitEvent(rt, stdout, "data", newBufferValue(rt, []byte(result.Stdout)))
				}
				if result.Stderr != "" {
					emitEvent(rt, stderr, "data", newBufferValue(rt, []byte(result.Stderr)))
				}
				emitEvent(rt, cp, "exit", rt.ToValue(result.Code))
				emitEvent(rt, cp, "close", rt.ToValue(result.Code))
			})
		}()
		return cp
	})

	return m
}

func (h *shimHost) parseExecOptions(rt *sobek.Runtime, call sobek.FunctionCall, startIdx int) *extensions.ExecOptions {
	opts := &extensions.ExecOptions{CWD: h.cwd}
	for i := startIdx; i < len(call.Arguments); i++ {
		arg := call.Argument(i)
		if arg == nil || sobek.IsUndefined(arg) || sobek.IsNull(arg) {
			continue
		}
		if _, ok := sobek.AssertFunction(arg); ok {
			continue
		}
		o := arg.ToObject(rt)
		if cwdVal := o.Get("cwd"); cwdVal != nil && !sobek.IsUndefined(cwdVal) {
			opts.CWD = cwdVal.String()
		}
		if envVal := o.Get("env"); envVal != nil && !sobek.IsUndefined(envVal) && !sobek.IsNull(envVal) {
			if envMap, ok := envVal.Export().(map[string]any); ok {
				opts.Env = make([]string, 0, len(envMap))
				for k, v := range envMap {
					opts.Env = append(opts.Env, k+"="+fmt.Sprint(v))
				}
			}
		}
		if timeoutVal := o.Get("timeout"); timeoutVal != nil && !sobek.IsUndefined(timeoutVal) {
			opts.Timeout = timeoutVal.ToInteger()
		}
		break
	}
	return opts
}

// --- readline module ---

// newReadlineModule supports the createInterface({ input }) + for-await usage
// in upstream examples. The fs.createReadStream shim delivers the whole file
// synchronously via .on("data"), so the interface iterates buffered lines.
func newReadlineModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	mustSet(rt, m, "createInterface", func(call sobek.FunctionCall) sobek.Value {
		var data []byte
		if options := call.Argument(0); present(options) {
			input := options.ToObject(rt).Get("input")
			if present(input) {
				inputObject := input.ToObject(rt)
				if on, ok := sobek.AssertFunction(inputObject.Get("on")); ok {
					collect := rt.ToValue(func(inner sobek.FunctionCall) sobek.Value {
						data = append(data, exportBytes(rt, inner.Argument(0))...)
						return sobek.Undefined()
					})
					_, _ = on(inputObject, rt.ToValue("data"), collect)
				}
			}
		}
		// Node readline emits one event per line with the terminator removed
		// and no trailing empty line for a trailing newline.
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		lines := strings.Split(text, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		values := make([]any, len(lines))
		for i, line := range lines {
			values[i] = line
		}
		factoryValue, err := rt.RunString(`(function (lines) {
			var index = 0;
			var rl = {
				close: function () {},
				on: function () { return rl; },
			};
			rl[Symbol.asyncIterator] = function () {
				return {
					next: function () {
						if (index < lines.length) {
							var value = lines[index];
							index += 1;
							return Promise.resolve({ value: value, done: false });
						}
						return Promise.resolve({ value: undefined, done: true });
					},
				};
			};
			return rl;
		})`)
		if err != nil {
			panic(rt.NewTypeError(err.Error()))
		}
		factory, ok := sobek.AssertFunction(factoryValue)
		if !ok {
			panic(rt.NewTypeError("readline interface factory is unavailable"))
		}
		value, err := factory(sobek.Undefined(), rt.NewArray(values...))
		if err != nil {
			panic(err)
		}
		return value
	})
	return m
}

// --- events module ---

func newEventsModule(rt *sobek.Runtime) *sobek.Object {
	m := rt.NewObject()
	ctor := func(call sobek.ConstructorCall) *sobek.Object {
		populateEventEmitter(rt, call.This)
		return nil
	}
	mustSet(rt, m, "EventEmitter", ctor)
	mustSet(rt, m, "default", ctor)
	return m
}

// emitEvent invokes an emitter's own emit method from Go (used to drive
// EventEmitter-backed shim objects like child processes).
func emitEvent(rt *sobek.Runtime, emitter *sobek.Object, event string, args ...sobek.Value) {
	fn, ok := sobek.AssertFunction(emitter.Get("emit"))
	if !ok {
		return
	}
	callArgs := append([]sobek.Value{rt.ToValue(event)}, args...)
	_, _ = fn(emitter, callArgs...)
}

func populateEventEmitter(rt *sobek.Runtime, emitter *sobek.Object) {
	type listener struct {
		fn   sobek.Callable
		val  sobek.Value
		once bool
	}
	listeners := make(map[string][]listener)

	addFn := func(call sobek.FunctionCall, once bool) sobek.Value {
		event := call.Argument(0).String()
		fnVal := call.Argument(1)
		fn, ok := sobek.AssertFunction(fnVal)
		if !ok {
			return emitter
		}
		listeners[event] = append(listeners[event], listener{fn: fn, val: fnVal, once: once})
		return emitter
	}
	removeFn := func(call sobek.FunctionCall) sobek.Value {
		event := call.Argument(0).String()
		fnVal := call.Argument(1)
		list := listeners[event]
		for i := len(list) - 1; i >= 0; i-- {
			if list[i].val.SameAs(fnVal) {
				listeners[event] = append(list[:i], list[i+1:]...)
				break
			}
		}
		return emitter
	}

	mustSet(rt, emitter, "on", func(call sobek.FunctionCall) sobek.Value { return addFn(call, false) })
	mustSet(rt, emitter, "addListener", func(call sobek.FunctionCall) sobek.Value { return addFn(call, false) })
	mustSet(rt, emitter, "once", func(call sobek.FunctionCall) sobek.Value { return addFn(call, true) })
	mustSet(rt, emitter, "off", removeFn)
	mustSet(rt, emitter, "removeListener", removeFn)
	mustSet(rt, emitter, "removeAllListeners", func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) > 0 && !sobek.IsUndefined(call.Argument(0)) {
			delete(listeners, call.Argument(0).String())
		} else {
			for k := range listeners {
				delete(listeners, k)
			}
		}
		return emitter
	})
	mustSet(rt, emitter, "emit", func(call sobek.FunctionCall) sobek.Value {
		event := call.Argument(0).String()
		list := listeners[event]
		if len(list) == 0 {
			return rt.ToValue(false)
		}
		args := call.Arguments[1:]
		var kept []listener
		for _, l := range list {
			_, _ = l.fn(sobek.Undefined(), args...)
			if !l.once {
				kept = append(kept, l)
			}
		}
		listeners[event] = kept
		return rt.ToValue(true)
	})
	mustSet(rt, emitter, "listenerCount", func(call sobek.FunctionCall) sobek.Value {
		return rt.ToValue(len(listeners[call.Argument(0).String()]))
	})
}

// --- Buffer global ---

func installBuffer(rt *sobek.Runtime) error {
	buf := rt.NewObject()
	mustSet(rt, buf, "from", func(call sobek.FunctionCall) sobek.Value {
		arg := call.Argument(0)
		switch v := arg.Export().(type) {
		case string:
			return newBufferValue(rt, []byte(v))
		case []byte:
			return newBufferValue(rt, v)
		default:
			if arr, ok := v.([]any); ok {
				b := make([]byte, len(arr))
				for i, item := range arr {
					if n, ok := item.(int64); ok {
						b[i] = byte(n)
					} else if n, ok := item.(float64); ok {
						b[i] = byte(int(n))
					}
				}
				return newBufferValue(rt, b)
			}
			return newBufferValue(rt, []byte(arg.String()))
		}
	})
	mustSet(rt, buf, "alloc", func(call sobek.FunctionCall) sobek.Value {
		size := int(call.Argument(0).ToInteger())
		return newBufferValue(rt, make([]byte, size))
	})
	mustSet(rt, buf, "allocUnsafe", func(call sobek.FunctionCall) sobek.Value {
		size := int(call.Argument(0).ToInteger())
		return newBufferValue(rt, make([]byte, size))
	})
	mustSet(rt, buf, "concat", func(call sobek.FunctionCall) sobek.Value {
		arr := call.Argument(0)
		if arr == nil || sobek.IsUndefined(arr) {
			return newBufferValue(rt, nil)
		}
		var combined []byte
		if items, ok := arr.Export().([]any); ok {
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					if data, ok := m["_data"].([]byte); ok {
						combined = append(combined, data...)
					}
				}
			}
		}
		return newBufferValue(rt, combined)
	})
	mustSet(rt, buf, "byteLength", func(call sobek.FunctionCall) sobek.Value {
		arg := call.Argument(0)
		switch v := arg.Export().(type) {
		case string:
			return rt.ToValue(len(v))
		default:
			return rt.ToValue(len(arg.String()))
		}
	})
	mustSet(rt, buf, "isBuffer", func(call sobek.FunctionCall) sobek.Value {
		arg := call.Argument(0)
		if arg == nil || sobek.IsUndefined(arg) || sobek.IsNull(arg) {
			return rt.ToValue(false)
		}
		if obj, ok := arg.(*sobek.Object); ok {
			return rt.ToValue(obj.Get("_isBuffer") != nil && obj.Get("_isBuffer").ToBoolean())
		}
		return rt.ToValue(false)
	})
	return rt.Set("Buffer", buf)
}

func newBufferModule(rt *sobek.Runtime) *sobek.Object {
	module := rt.NewObject()
	mustSet(rt, module, "Buffer", rt.Get("Buffer"))
	return module
}

func newBufferValue(rt *sobek.Runtime, data []byte) sobek.Value {
	obj := rt.NewObject()
	mustSet(rt, obj, "_data", data)
	mustSet(rt, obj, "_isBuffer", true)
	mustSet(rt, obj, "length", len(data))
	mustSet(rt, obj, "byteLength", len(data))
	mustSet(rt, obj, "toString", func(call sobek.FunctionCall) sobek.Value {
		return rt.ToValue(string(data))
	})
	mustSet(rt, obj, "slice", func(call sobek.FunctionCall) sobek.Value {
		n := len(data)
		start := 0
		if len(call.Arguments) > 0 {
			start = int(call.Argument(0).ToInteger())
			if start < 0 {
				start += n
			}
			if start < 0 {
				start = 0
			}
		}
		end := n
		if len(call.Arguments) > 1 {
			end = int(call.Argument(1).ToInteger())
			if end < 0 {
				end += n
			}
			if end > n {
				end = n
			}
		}
		if start > end {
			start = end
		}
		return newBufferValue(rt, data[start:end])
	})
	mustSet(rt, obj, "subarray", obj.Get("slice"))
	return obj
}

// --- fetch global ---

var (
	fetchClient          = &http.Client{Timeout: 30 * time.Second}
	fetchMaxResponseBody = 50 << 20 // ponytail: raise if a real extension needs more
)

// streamBody wraps an HTTP response body for incremental streaming with size enforcement.
type streamBody struct {
	body      io.ReadCloser
	bodyMu    sync.Mutex
	maxSize   int
	totalRead int
	closed    bool // protected by bodyMu
	cancelCtx context.CancelFunc
}

func newStreamBody(body io.ReadCloser, maxSize int, ctx context.Context, cancel context.CancelFunc, vm *runtimeVM) *streamBody {
	sb := &streamBody{body: body, maxSize: maxSize, cancelCtx: cancel}
	vm.wg.Add(1)
	go func() {
		defer vm.wg.Done()
		<-ctx.Done()
		sb.bodyMu.Lock()
		if !sb.closed {
			sb.closed = true
			_ = sb.body.Close()
		}
		sb.bodyMu.Unlock()
	}()
	return sb
}

func (sb *streamBody) readChunk() ([]byte, bool, error) {
	sb.bodyMu.Lock()
	defer sb.bodyMu.Unlock()
	if sb.closed {
		return nil, true, nil
	}
	buf := make([]byte, 65536)
	n, err := sb.body.Read(buf)
	if n > 0 {
		sb.totalRead += n
		if sb.totalRead > sb.maxSize {
			sb.closed = true
			_ = sb.body.Close()
			return nil, false, fmt.Errorf("response body exceeds %d byte limit", sb.maxSize)
		}
	}
	if err != nil {
		sb.closed = true
		_ = sb.body.Close()
		if err == io.EOF {
			return buf[:n], true, nil
		}
		return nil, false, err
	}
	return buf[:n], false, nil
}

func (sb *streamBody) drainAll() ([]byte, error) {
	sb.bodyMu.Lock()
	defer sb.bodyMu.Unlock()
	if sb.closed {
		return nil, nil
	}
	remaining := int64(sb.maxSize-sb.totalRead) + 1
	data, err := io.ReadAll(io.LimitReader(sb.body, remaining))
	sb.totalRead += len(data)
	sb.closed = true
	_ = sb.body.Close()
	if sb.totalRead > sb.maxSize {
		return nil, fmt.Errorf("response body exceeds %d byte limit", sb.maxSize)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (sb *streamBody) close() { sb.cancelCtx() }

func (h *shimHost) installFetch(rt *sobek.Runtime) error {
	// Headers constructor
	if err := rt.Set("Headers", func(call sobek.ConstructorCall) *sobek.Object {
		hdr := make(http.Header)
		if len(call.Arguments) > 0 && !sobek.IsUndefined(call.Arguments[0]) && !sobek.IsNull(call.Arguments[0]) {
			parseHeaders(rt, call.Arguments[0], hdr)
		}
		obj := newHeadersObject(rt, hdr)
		_ = obj.SetPrototype(call.This.Prototype())
		return obj
	}); err != nil {
		return err
	}

	// Request constructor
	if err := rt.Set("Request", func(call sobek.ConstructorCall) *sobek.Object {
		urlStr := call.Arguments[0].String()
		method := "GET"
		var bodyStr string
		hasBody := false
		reqHeaders := make(http.Header)
		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Arguments[1]) {
			opts := call.Arguments[1].ToObject(rt)
			if m := opts.Get("method"); m != nil && !sobek.IsUndefined(m) {
				method = strings.ToUpper(m.String())
			}
			if b := opts.Get("body"); b != nil && !sobek.IsUndefined(b) {
				bodyStr = b.String()
				hasBody = true
			}
			if hv := opts.Get("headers"); hv != nil && !sobek.IsUndefined(hv) {
				parseHeaders(rt, hv, reqHeaders)
			}
		}
		mustSet(rt, call.This, "url", urlStr)
		mustSet(rt, call.This, "method", method)
		mustSet(rt, call.This, "headers", newHeadersObject(rt, reqHeaders))
		mustSet(rt, call.This, "_isRequest", true)
		if hasBody {
			mustSet(rt, call.This, "_body", bodyStr)
			mustSet(rt, call.This, "_hasBody", true)
		}
		return nil
	}); err != nil {
		return err
	}

	// Response constructor
	if err := rt.Set("Response", func(call sobek.ConstructorCall) *sobek.Object {
		body := ""
		if len(call.Arguments) > 0 && !sobek.IsUndefined(call.Arguments[0]) && !sobek.IsNull(call.Arguments[0]) {
			body = call.Arguments[0].String()
		}
		status := 200
		respHeaders := make(http.Header)
		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Arguments[1]) {
			opts := call.Arguments[1].ToObject(rt)
			if s := opts.Get("status"); s != nil && !sobek.IsUndefined(s) {
				status = int(s.ToInteger())
			}
			if hv := opts.Get("headers"); hv != nil && !sobek.IsUndefined(hv) {
				parseHeaders(rt, hv, respHeaders)
			}
		}
		mustSet(rt, call.This, "status", status)
		mustSet(rt, call.This, "ok", status >= 200 && status < 300)
		mustSet(rt, call.This, "statusText", http.StatusText(status))
		mustSet(rt, call.This, "headers", newHeadersObject(rt, respHeaders))
		installInMemoryResponseBody(rt, call.This, []byte(body))
		return nil
	}); err != nil {
		return err
	}

	return rt.Set("fetch", func(call sobek.FunctionCall) sobek.Value {
		urlStr := ""
		method := "GET"
		var bodyStr string
		var hasBody bool
		reqHeaders := make(http.Header)

		arg0 := call.Argument(0)
		if obj, ok := arg0.(*sobek.Object); ok && obj.Get("_isRequest") != nil && obj.Get("_isRequest").ToBoolean() {
			urlStr = obj.Get("url").String()
			method = obj.Get("method").String()
			if hb := obj.Get("_hasBody"); hb != nil && hb.ToBoolean() {
				bodyStr = obj.Get("_body").String()
				hasBody = true
			}
			hdrObj := obj.Get("headers").ToObject(rt)
			if entriesFn, ok2 := sobek.AssertFunction(hdrObj.Get("entries")); ok2 {
				entriesVal, _ := entriesFn(hdrObj)
				if arr, ok3 := entriesVal.Export().([]any); ok3 {
					for _, item := range arr {
						if pair, ok4 := item.([]any); ok4 && len(pair) >= 2 {
							reqHeaders.Set(fmt.Sprint(pair[0]), fmt.Sprint(pair[1]))
						}
					}
				}
			}
		} else {
			urlStr = arg0.String()
		}

		if len(call.Arguments) > 1 && !sobek.IsUndefined(call.Argument(1)) {
			opts := call.Argument(1).ToObject(rt)
			if m := opts.Get("method"); m != nil && !sobek.IsUndefined(m) {
				method = strings.ToUpper(m.String())
			}
			if b := opts.Get("body"); b != nil && !sobek.IsUndefined(b) {
				bodyStr = b.String()
				hasBody = true
			}
			if hv := opts.Get("headers"); hv != nil && !sobek.IsUndefined(hv) {
				parseHeaders(rt, hv, reqHeaders)
			}
		}

		promise, resolve, reject := rt.NewPromise()

		h.vm.wg.Add(1)
		go func() {
			defer h.vm.wg.Done()
			var reqBody io.Reader
			if hasBody {
				reqBody = strings.NewReader(bodyStr)
			}
			bodyCtx, bodyCancel := context.WithCancel(h.vm.rootCtx)
			req, err := http.NewRequestWithContext(bodyCtx, method, urlStr, reqBody)
			if err != nil {
				bodyCancel()
				h.vm.postCallback(func(rt *sobek.Runtime) { _ = reject(rt.NewTypeError(err.Error())) })
				return
			}
			req.Header = reqHeaders
			resp, err := fetchClient.Do(req)
			if err != nil {
				bodyCancel()
				h.vm.postCallback(func(rt *sobek.Runtime) { _ = reject(rt.NewTypeError(err.Error())) })
				return
			}
			statusCode := resp.StatusCode
			respHeaders := resp.Header.Clone()
			sb := newStreamBody(resp.Body, fetchMaxResponseBody, bodyCtx, bodyCancel, h.vm)

			h.vm.postCallback(func(rt *sobek.Runtime) {
				response := rt.NewObject()
				mustSet(rt, response, "status", statusCode)
				mustSet(rt, response, "ok", statusCode >= 200 && statusCode < 300)
				mustSet(rt, response, "statusText", http.StatusText(statusCode))
				mustSet(rt, response, "url", urlStr)
				mustSet(rt, response, "headers", newHeadersObject(rt, respHeaders))
				installStreamResponseBody(rt, response, sb, h.vm)
				_ = resolve(response)
			})
		}()

		return rt.ToValue(promise)
	})
}

func newHeadersObject(rt *sobek.Runtime, h http.Header) *sobek.Object {
	headers := rt.NewObject()
	mustSet(rt, headers, "get", func(call sobek.FunctionCall) sobek.Value {
		key := http.CanonicalHeaderKey(call.Argument(0).String())
		vs, ok := h[key]
		if !ok {
			return sobek.Null()
		}
		return rt.ToValue(strings.Join(vs, ", "))
	})
	mustSet(rt, headers, "has", func(call sobek.FunctionCall) sobek.Value {
		key := http.CanonicalHeaderKey(call.Argument(0).String())
		_, ok := h[key]
		return rt.ToValue(ok)
	})
	mustSet(rt, headers, "set", func(call sobek.FunctionCall) sobek.Value {
		h.Set(call.Argument(0).String(), call.Argument(1).String())
		return sobek.Undefined()
	})
	mustSet(rt, headers, "append", func(call sobek.FunctionCall) sobek.Value {
		h.Add(call.Argument(0).String(), call.Argument(1).String())
		return sobek.Undefined()
	})
	mustSet(rt, headers, "delete", func(call sobek.FunctionCall) sobek.Value {
		h.Del(call.Argument(0).String())
		return sobek.Undefined()
	})
	mustSet(rt, headers, "forEach", func(call sobek.FunctionCall) sobek.Value {
		fn, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			return sobek.Undefined()
		}
		keys := sortedHeaderKeys(h)
		for _, k := range keys {
			_, _ = fn(sobek.Undefined(), rt.ToValue(strings.Join(h[k], ", ")), rt.ToValue(strings.ToLower(k)))
		}
		return sobek.Undefined()
	})
	mustSet(rt, headers, "entries", func(sobek.FunctionCall) sobek.Value {
		keys := sortedHeaderKeys(h)
		entries := make([]any, len(keys))
		for i, k := range keys {
			entries[i] = []any{strings.ToLower(k), strings.Join(h[k], ", ")}
		}
		return rt.ToValue(entries)
	})
	return headers
}

// parseHeaders accepts object {k: v}, array of [k, v] pairs, or a Headers-like
// object with forEach, and populates target.
func parseHeaders(rt *sobek.Runtime, value sobek.Value, target http.Header) {
	exported := value.Export()
	switch v := exported.(type) {
	case map[string]any:
		for k, val := range v {
			target.Set(k, fmt.Sprint(val))
		}
	case []any:
		for _, item := range v {
			if pair, ok := item.([]any); ok && len(pair) >= 2 {
				target.Set(fmt.Sprint(pair[0]), fmt.Sprint(pair[1]))
			}
		}
	default:
		// Headers-like with forEach
		obj := value.ToObject(rt)
		forEach, ok := sobek.AssertFunction(obj.Get("forEach"))
		if ok {
			_, _ = forEach(obj, rt.ToValue(func(call sobek.FunctionCall) sobek.Value {
				val := call.Argument(0).String()
				key := call.Argument(1).String()
				target.Set(key, val)
				return sobek.Undefined()
			}))
		}
	}
}

// --- console global ---

func installConsole(rt *sobek.Runtime) error {
	c := rt.NewObject()
	printer := func(prefix string) func(sobek.FunctionCall) sobek.Value {
		return func(call sobek.FunctionCall) sobek.Value {
			parts := make([]string, len(call.Arguments))
			for i, a := range call.Arguments {
				parts[i] = a.String()
			}
			fmt.Fprintln(os.Stderr, prefix+strings.Join(parts, " "))
			return sobek.Undefined()
		}
	}
	mustSet(rt, c, "log", printer(""))
	mustSet(rt, c, "info", printer(""))
	mustSet(rt, c, "warn", printer("[warn] "))
	mustSet(rt, c, "error", printer("[error] "))
	mustSet(rt, c, "debug", printer("[debug] "))
	mustSet(rt, c, "trace", printer("[trace] "))
	return rt.Set("console", c)
}

// --- timers ---

func (h *shimHost) installTimers(rt *sobek.Runtime) error {
	if err := rt.Set("setTimeout", func(call sobek.FunctionCall) sobek.Value {
		fn, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			return sobek.Undefined()
		}
		delay := int64(0)
		if len(call.Arguments) > 1 {
			delay = call.Argument(1).ToInteger()
		}
		var args []sobek.Value
		if len(call.Arguments) > 2 {
			args = call.Arguments[2:]
		}
		id := h.vm.allocID()
		ctx, cancel := context.WithCancel(h.vm.rootCtx)
		h.vm.mu.Lock()
		h.vm.ops[id] = cancel
		h.vm.mu.Unlock()

		h.vm.wg.Add(1)
		go func() {
			defer h.vm.wg.Done()
			if delay > 0 {
				t := time.NewTimer(time.Duration(delay) * time.Millisecond)
				select {
				case <-t.C:
				case <-ctx.Done():
					t.Stop()
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			h.vm.postCallback(func(rt *sobek.Runtime) {
				select {
				case <-ctx.Done():
					return
				default:
				}
				h.vm.mu.Lock()
				delete(h.vm.ops, id)
				h.vm.mu.Unlock()
				_, _ = fn(sobek.Undefined(), args...)
			})
		}()
		return rt.ToValue(id)
	}); err != nil {
		return err
	}
	if err := rt.Set("clearTimeout", func(call sobek.FunctionCall) sobek.Value {
		h.vm.cancelOp(call.Argument(0).ToInteger())
		return sobek.Undefined()
	}); err != nil {
		return err
	}
	if err := rt.Set("setInterval", func(call sobek.FunctionCall) sobek.Value {
		fn, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			return sobek.Undefined()
		}
		interval := call.Argument(1).ToInteger()
		if interval < 1 {
			interval = 1
		}
		var args []sobek.Value
		if len(call.Arguments) > 2 {
			args = call.Arguments[2:]
		}
		id := h.vm.allocID()
		ctx, cancel := context.WithCancel(h.vm.rootCtx)
		h.vm.mu.Lock()
		h.vm.ops[id] = cancel
		h.vm.mu.Unlock()

		h.vm.wg.Add(1)
		go func() {
			defer h.vm.wg.Done()
			ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					h.vm.postCallback(func(rt *sobek.Runtime) {
						select {
						case <-ctx.Done():
							return
						default:
						}
						_, _ = fn(sobek.Undefined(), args...)
					})
				case <-ctx.Done():
					return
				}
			}
		}()
		return rt.ToValue(id)
	}); err != nil {
		return err
	}
	return rt.Set("clearInterval", func(call sobek.FunctionCall) sobek.Value {
		h.vm.cancelOp(call.Argument(0).ToInteger())
		return sobek.Undefined()
	})
}

// --- helpers ---

// newReadableBodyObject creates a minimal ReadableStream-like body from buffered data.
// Provides getReader() → {read()} yielding {value: Uint8Array, done: bool} chunks.
func installStreamResponseBody(rt *sobek.Runtime, response *sobek.Object, sb *streamBody, vm *runtimeVM) {
	bodyUsed := false
	mustSet(rt, response, "bodyUsed", false)

	mustSet(rt, response, "text", func(sobek.FunctionCall) sobek.Value {
		if bodyUsed {
			return rejectPromise(rt, fmt.Errorf("body already consumed"))
		}
		bodyUsed = true
		mustSet(rt, response, "bodyUsed", true)
		promise, resolve, reject := rt.NewPromise()
		vm.wg.Add(1)
		go func() {
			defer vm.wg.Done()
			data, err := sb.drainAll()
			vm.postCallback(func(rt *sobek.Runtime) {
				if err != nil {
					_ = reject(rt.NewTypeError(err.Error()))
					return
				}
				_ = resolve(rt.ToValue(string(data)))
			})
		}()
		return rt.ToValue(promise)
	})

	mustSet(rt, response, "json", func(sobek.FunctionCall) sobek.Value {
		if bodyUsed {
			return rejectPromise(rt, fmt.Errorf("body already consumed"))
		}
		bodyUsed = true
		mustSet(rt, response, "bodyUsed", true)
		promise, resolve, reject := rt.NewPromise()
		vm.wg.Add(1)
		go func() {
			defer vm.wg.Done()
			data, err := sb.drainAll()
			vm.postCallback(func(rt *sobek.Runtime) {
				if err != nil {
					_ = reject(rt.NewTypeError(err.Error()))
					return
				}
				var parsed any
				if err := json.Unmarshal(data, &parsed); err != nil {
					_ = reject(rt.NewTypeError(err.Error()))
					return
				}
				_ = resolve(rt.ToValue(parsed))
			})
		}()
		return rt.ToValue(promise)
	})

	mustSet(rt, response, "arrayBuffer", func(sobek.FunctionCall) sobek.Value {
		if bodyUsed {
			return rejectPromise(rt, fmt.Errorf("body already consumed"))
		}
		bodyUsed = true
		mustSet(rt, response, "bodyUsed", true)
		promise, resolve, reject := rt.NewPromise()
		vm.wg.Add(1)
		go func() {
			defer vm.wg.Done()
			data, err := sb.drainAll()
			vm.postCallback(func(rt *sobek.Runtime) {
				if err != nil {
					_ = reject(rt.NewTypeError(err.Error()))
					return
				}
				_ = resolve(rt.ToValue(rt.NewArrayBuffer(data)))
			})
		}()
		return rt.ToValue(promise)
	})

	body := rt.NewObject()
	mustSet(rt, body, "getReader", func(sobek.FunctionCall) sobek.Value {
		if bodyUsed {
			panic(rt.NewTypeError("body already consumed"))
		}
		bodyUsed = true
		mustSet(rt, response, "bodyUsed", true)
		return newStreamReader(rt, sb, vm)
	})
	mustSet(rt, response, "body", body)
}

func newStreamReader(rt *sobek.Runtime, sb *streamBody, vm *runtimeVM) *sobek.Object {
	reader := rt.NewObject()
	done := false
	mustSet(rt, reader, "read", func(sobek.FunctionCall) sobek.Value {
		if done {
			result := rt.NewObject()
			mustSet(rt, result, "value", sobek.Undefined())
			mustSet(rt, result, "done", true)
			return resolvePromise(rt, result)
		}
		promise, resolve, reject := rt.NewPromise()
		vm.wg.Add(1)
		go func() {
			defer vm.wg.Done()
			chunk, eof, err := sb.readChunk()
			vm.postCallback(func(rt *sobek.Runtime) {
				if err != nil {
					done = true
					_ = reject(rt.NewTypeError(err.Error()))
					return
				}
				result := rt.NewObject()
				if eof && len(chunk) == 0 {
					done = true
					mustSet(rt, result, "value", sobek.Undefined())
					mustSet(rt, result, "done", true)
				} else {
					if eof {
						done = true
					}
					ab := rt.NewArrayBuffer(chunk)
					uint8Array, err := rt.New(rt.Get("Uint8Array"), rt.ToValue(ab))
					if err != nil {
						mustSet(rt, result, "value", rt.ToValue(ab))
					} else {
						mustSet(rt, result, "value", uint8Array)
					}
					mustSet(rt, result, "done", false)
				}
				_ = resolve(result)
			})
		}()
		return rt.ToValue(promise)
	})
	mustSet(rt, reader, "cancel", func(sobek.FunctionCall) sobek.Value {
		done = true
		sb.close()
		return resolvePromise(rt, sobek.Undefined())
	})
	mustSet(rt, reader, "releaseLock", func(sobek.FunctionCall) sobek.Value {
		return sobek.Undefined()
	})
	return reader
}

func installInMemoryResponseBody(rt *sobek.Runtime, response *sobek.Object, data []byte) {
	bodyUsed := false
	text := string(data)
	mustSet(rt, response, "bodyUsed", false)

	mustSet(rt, response, "text", func(sobek.FunctionCall) sobek.Value {
		if bodyUsed {
			return rejectPromise(rt, fmt.Errorf("body already consumed"))
		}
		bodyUsed = true
		mustSet(rt, response, "bodyUsed", true)
		return resolvePromise(rt, rt.ToValue(text))
	})
	mustSet(rt, response, "json", func(sobek.FunctionCall) sobek.Value {
		if bodyUsed {
			return rejectPromise(rt, fmt.Errorf("body already consumed"))
		}
		bodyUsed = true
		mustSet(rt, response, "bodyUsed", true)
		var parsed any
		if err := json.Unmarshal(data, &parsed); err != nil {
			return rejectPromise(rt, err)
		}
		return resolvePromise(rt, rt.ToValue(parsed))
	})
	mustSet(rt, response, "arrayBuffer", func(sobek.FunctionCall) sobek.Value {
		if bodyUsed {
			return rejectPromise(rt, fmt.Errorf("body already consumed"))
		}
		bodyUsed = true
		mustSet(rt, response, "bodyUsed", true)
		return resolvePromise(rt, rt.ToValue(rt.NewArrayBuffer(data)))
	})

	body := rt.NewObject()
	mustSet(rt, body, "getReader", func(sobek.FunctionCall) sobek.Value {
		if bodyUsed {
			panic(rt.NewTypeError("body already consumed"))
		}
		bodyUsed = true
		mustSet(rt, response, "bodyUsed", true)
		reader := rt.NewObject()
		consumed := false
		mustSet(rt, reader, "read", func(sobek.FunctionCall) sobek.Value {
			result := rt.NewObject()
			if consumed {
				mustSet(rt, result, "value", sobek.Undefined())
				mustSet(rt, result, "done", true)
			} else {
				consumed = true
				ab := rt.NewArrayBuffer(data)
				uint8Array, err := rt.New(rt.Get("Uint8Array"), rt.ToValue(ab))
				if err != nil {
					mustSet(rt, result, "value", rt.ToValue(ab))
				} else {
					mustSet(rt, result, "value", uint8Array)
				}
				mustSet(rt, result, "done", false)
			}
			return resolvePromise(rt, result)
		})
		mustSet(rt, reader, "releaseLock", func(sobek.FunctionCall) sobek.Value {
			return sobek.Undefined()
		})
		mustSet(rt, reader, "cancel", func(sobek.FunctionCall) sobek.Value {
			consumed = true
			return resolvePromise(rt, sobek.Undefined())
		})
		return reader
	})
	mustSet(rt, response, "body", body)
}

func sortedHeaderKeys(h http.Header) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func nodeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	case "arm":
		return "arm"
	default:
		return runtime.GOARCH
	}
}

func mustSet(rt *sobek.Runtime, obj *sobek.Object, name string, value any) {
	if err := obj.Set(name, value); err != nil {
		panic(rt.NewTypeError(err.Error()))
	}
}

func resolvePromise(rt *sobek.Runtime, value sobek.Value) sobek.Value {
	promise, resolve, _ := rt.NewPromise()
	_ = resolve(value)
	return rt.ToValue(promise)
}

func rejectPromise(rt *sobek.Runtime, err error) sobek.Value {
	promise, _, reject := rt.NewPromise()
	_ = reject(rt.NewTypeError(err.Error()))
	return rt.ToValue(promise)
}

func hasEncoding(rt *sobek.Runtime, call sobek.FunctionCall, idx int) bool {
	if len(call.Arguments) <= idx {
		return false
	}
	arg := call.Argument(idx)
	if arg == nil || sobek.IsUndefined(arg) || sobek.IsNull(arg) {
		return false
	}
	s := arg.String()
	if s != "" && s != "undefined" && s != "[object Object]" {
		return true
	}
	if obj, ok := arg.(*sobek.Object); ok {
		enc := obj.Get("encoding")
		if enc != nil && !sobek.IsUndefined(enc) && !sobek.IsNull(enc) {
			return enc.String() != ""
		}
	}
	return false
}

func exportBytes(rt *sobek.Runtime, value sobek.Value) []byte {
	if value == nil || sobek.IsUndefined(value) {
		return nil
	}
	if obj, ok := value.(*sobek.Object); ok {
		if data := obj.Get("_data"); data != nil && !sobek.IsUndefined(data) {
			if b, ok := data.Export().([]byte); ok {
				return b
			}
		}
	}
	return []byte(value.String())
}
