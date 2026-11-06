// Orb for the desktop (macOS, Linux and Windows): one native renderer of `orb app` (bridge/view),
// drawn by MyGo in pure Go. It holds no logic of its own; the view, shared with every Orb app,
// does. build.sh assembles the apps with the orb they run.
package main

import (
	"log"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	// One app per user: two would each run `orb app` over the same Bridge and save its tabs over the other's.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	a := &app{
		drafts: map[string]string{}, found: map[string][]Completion{}, foundFor: map[string]string{}, lists: map[string]*ui.ListState{}, shells: map[string]*shell{},
		answer: map[string]string{}, expanded: map[string]bool{}, details: map[string]map[string]string{},
		pics: map[string]*ui.Bitmap{}, loading: map[string]bool{}, fold: map[string]bool{}, side: 272, asked: map[string]time.Time{},
		defaults: map[string]*Tab{},
	}
	a.loadTiles()
	// `orb app` (after the login shell's environment, a third of a second or more) starts now and
	// boots while the window opens.
	a.o = newOrb(a.notify)
	// A pairing code opened as a link (orb-bridge:…) joins from the Bridge screen; on Linux and
	// Windows the link starts the app again, which hands it to this one.
	join := func(url string) {
		if strings.HasPrefix(url, "orb-bridge:") && a.o.win != nil {
			a.o.win.Update(func() { a.push(screen{kind: "join", arg: url}) })
		}
	}
	mygo.App.OnOpenURL(join)
	mygo.App.OnSecondInstance(func(args []string, _ string) {
		for _, arg := range args {
			join(arg)
		}
		if a.o.win != nil {
			a.o.win.Focus()
		}
	})
	mygo.App.WhenReady(func() {
		loadFont()
		win := mygo.NewWindow(mygo.WindowOptions{
			Title: "Orb", Width: 1080, Height: 760, StateKey: "main",
			TitleBarStyle: mygo.TitleBarHidden,
			Content:       ui.View(a.view),
		})
		a.o.open(win)
		// The view follows what shows at full pace, and the rest at the pace of a window out of sight.
		win.OnFocus(func() { a.o.setVisible(true) })
		win.OnBlur(func() { a.o.setVisible(false) })
		a.o.setVisible(true)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// notify tells of a turn ending or a question arriving out of sight; clicking it shows the conversation.
func (a *app) notify(tab, title, text string) {
	n := mygo.NewNotification(mygo.NotificationOptions{ID: tab, Title: title, Body: text})
	n.OnClick(func() {
		a.o.win.Update(func() { a.show(tab) })
		a.o.win.Focus()
	})
	_ = n.Show()
}
