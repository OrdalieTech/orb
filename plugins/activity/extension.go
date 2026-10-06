package activity

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent/extensions"
)

// View is supplied by the presentation assembly; the activity module stays headless.
type View interface {
	extensions.Component
	Control(string) error
}
type ViewFactory func(*Store, extensions.UIHost, extensions.Theme) View

func Extension(factory ViewFactory) extensions.Factory {
	return func(api extensions.API) error {
		var mu sync.Mutex
		var store *Store
		var ui extensions.UI
		var view View
		var refresh func()
		closed := false
		show := func() {
			if ui == nil || store == nil || factory == nil {
				return
			}
			if len(store.Snapshot()) == 0 {
				ui.SetWidget("activity", nil, nil)
				view, refresh = nil, nil
			} else if view == nil {
				ui.SetWidget("activity", &extensions.Widget{Factory: func(host extensions.UIHost, theme extensions.Theme) extensions.Component {
					view = factory(store, host, theme)
					refresh = host.Invalidate
					return view
				}}, nil)
			} else if refresh != nil {
				refresh()
			}
		}
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			id := ctx.SessionManager().GetSessionID()
			if store == nil || store.sessionID != id {
				if ui != nil {
					ui.SetWidget("activity", nil, nil)
				}
				view, refresh = nil, nil
				store = NewStore(id)
			}
			closed = false
			if ctx.HasUI() && ctx.Mode() == extensions.ModeTUI {
				ui = ctx.UI()
			}
			show()
			return nil, nil
		})
		api.Events().On(Channel, func(_ context.Context, data any) error {
			record, ok := data.(Record)
			if !ok {
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			if !closed {
				// Earlier session_start handlers can already launch work.
				if store == nil {
					store = NewStore(record.SessionID)
				}
				if store.Apply(record) {
					show()
				}
			}
			return nil
		})
		api.On(extensions.EventInput, func(_ context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			if !closed && store != nil {
				store.ClearCompleted()
				show()
			}
			return nil, nil
		})
		control := func(action string) error {
			mu.Lock()
			defer mu.Unlock()
			if closed || ui == nil {
				return fmt.Errorf("/activity requires interactive mode")
			}
			if view == nil {
				ui.Notify("No activities in this session", extensions.NotifyInfo)
				return nil
			}
			return view.Control(strings.TrimSpace(action))
		}
		api.RegisterCommand("activity", extensions.Command{
			Description: "Expand or collapse activity statuses (next/prev to page, close to fold)",
			Handler:     func(_ context.Context, args string, _ extensions.CommandContext) error { return control(args) },
		})
		api.On(extensions.EventSessionShutdown, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			closed = true
			if ui != nil {
				ui.SetWidget("activity", nil, nil)
			}
			view, refresh, ui = nil, nil, nil
			return nil, nil
		})
		return nil
	}
}
