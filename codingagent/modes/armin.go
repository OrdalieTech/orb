package modes

import (
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/tui"

	theme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
)

const (
	arminWidth         = 31
	arminHeight        = 36
	arminDisplayHeight = (arminHeight + 1) / 2
)

var arminBits = [...]byte{
	0xff, 0xff, 0xff, 0x7f, 0xff, 0xf0, 0xff, 0x7f, 0xff, 0xed, 0xff, 0x7f, 0xff, 0xdb, 0xff, 0x7f, 0xff, 0xb7, 0xff,
	0x7f, 0xff, 0x77, 0xfe, 0x7f, 0x3f, 0xf8, 0xfe, 0x7f, 0xdf, 0xff, 0xfe, 0x7f, 0xdf, 0x3f, 0xfc, 0x7f, 0x9f, 0xc3,
	0xfb, 0x7f, 0x6f, 0xfc, 0xf4, 0x7f, 0xf7, 0x0f, 0xf7, 0x7f, 0xf7, 0xff, 0xf7, 0x7f, 0xf7, 0xff, 0xe3, 0x7f, 0xf7,
	0x07, 0xe8, 0x7f, 0xef, 0xf8, 0x67, 0x70, 0x0f, 0xff, 0xbb, 0x6f, 0xf1, 0x00, 0xd0, 0x5b, 0xfd, 0x3f, 0xec, 0x53,
	0xc1, 0xff, 0xef, 0x57, 0x9f, 0xfd, 0xee, 0x5f, 0x9f, 0xfc, 0xae, 0x5f, 0x1f, 0x78, 0xac, 0x5f, 0x3f, 0x00, 0x50,
	0x6c, 0x7f, 0x00, 0xdc, 0x77, 0xff, 0xc0, 0x3f, 0x78, 0xff, 0x01, 0xf8, 0x7f, 0xff, 0x03, 0x9c, 0x78, 0xff, 0x07,
	0x8c, 0x7c, 0xff, 0x0f, 0xce, 0x78, 0xff, 0xff, 0xcf, 0x7f, 0xff, 0xff, 0xcf, 0x78, 0xff, 0xff, 0xdf, 0x78, 0xff,
	0xff, 0xdf, 0x7d, 0xff, 0xff, 0x3f, 0x7e, 0xff, 0xff, 0xff, 0x7f,
}

type arminEffect string

const (
	arminTypewriter arminEffect = "typewriter"
	arminScanline   arminEffect = "scanline"
	arminRain       arminEffect = "rain"
	arminFade       arminEffect = "fade"
	arminCRT        arminEffect = "crt"
	arminGlitch     arminEffect = "glitch"
	arminDissolve   arminEffect = "dissolve"
)

var arminEffects = [...]arminEffect{
	arminTypewriter,
	arminScanline,
	arminRain,
	arminFade,
	arminCRT,
	arminGlitch,
	arminDissolve,
}

type arminPosition struct{ row, column int }
type arminDrop struct{ y, settled int }
type arminScheduler func(time.Duration, func()) func()

type ArminComponent struct {
	mu sync.Mutex

	ui        tui.RenderRequester
	random    func() float64
	effect    arminEffect
	final     [][]rune
	current   [][]rune
	position  int
	row       int
	drops     []arminDrop
	positions []arminPosition
	expansion int
	phase     int

	cachedLines   []string
	cachedWidth   int
	gridVersion   int
	cachedVersion int
	cancel        func()
	stopped       bool
}

func NewArminComponent(ui tui.RenderRequester) *ArminComponent {
	return newArminComponentWithHooks(ui, rand.Float64, scheduleArminAnimation)
}

func newArminComponentWithHooks(ui tui.RenderRequester, random func() float64, schedule arminScheduler) *ArminComponent {
	if random == nil {
		random = rand.Float64
	}
	choice := int(math.Floor(random() * float64(len(arminEffects))))
	choice = max(0, min(len(arminEffects)-1, choice))
	component := &ArminComponent{
		ui:            ui,
		random:        random,
		effect:        arminEffects[choice],
		final:         buildArminGrid(),
		current:       emptyArminGrid(),
		cachedVersion: -1,
	}
	component.initializeEffect()
	if schedule != nil {
		fps := 30
		if component.effect == arminGlitch {
			fps = 60
		}
		component.cancel = schedule(time.Second/time.Duration(fps), component.tickAnimation)
	}
	return component
}

func scheduleArminAnimation(interval time.Duration, callback func()) func() {
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-ticker.C:
				callback()
			case <-done:
				return
			}
		}
	}()
	return func() {
		once.Do(func() {
			ticker.Stop()
			close(done)
		})
	}
}

func (component *ArminComponent) Invalidate() {
	component.mu.Lock()
	component.cachedWidth = 0
	component.mu.Unlock()
}

func (component *ArminComponent) Render(width int) []string {
	component.mu.Lock()
	defer component.mu.Unlock()
	if width == component.cachedWidth && component.cachedVersion == component.gridVersion {
		return append([]string(nil), component.cachedLines...)
	}

	available := max(0, width-1)
	lines := make([]string, 0, arminDisplayHeight+1)
	for _, row := range component.current {
		clipped := row
		if len(clipped) > available {
			clipped = clipped[:available]
		}
		value := string(clipped)
		lines = append(lines, " "+theme.FG("accent", value)+strings.Repeat(" ", max(0, width-1-len(clipped))))
	}
	message := "ARMIN SAYS HI"
	lines = append(lines, " "+theme.FG("accent", message)+strings.Repeat(" ", max(0, width-1-len(message))))

	component.cachedLines = append(component.cachedLines[:0], lines...)
	component.cachedWidth = width
	component.cachedVersion = component.gridVersion
	return append([]string(nil), component.cachedLines...)
}

func (component *ArminComponent) Dispose() {
	component.stopAnimation()
}

func (component *ArminComponent) tickAnimation() {
	component.mu.Lock()
	if component.stopped {
		component.mu.Unlock()
		return
	}
	done := component.tickEffect()
	component.gridVersion++
	component.mu.Unlock()
	if component.ui != nil {
		component.ui.RequestRender()
	}
	if done {
		component.stopAnimation()
	}
}

func (component *ArminComponent) stopAnimation() {
	component.mu.Lock()
	if component.stopped {
		component.mu.Unlock()
		return
	}
	component.stopped = true
	cancel := component.cancel
	component.cancel = nil
	component.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (component *ArminComponent) initializeEffect() {
	switch component.effect {
	case arminRain:
		component.drops = make([]arminDrop, arminWidth)
		for column := range component.drops {
			component.drops[column].y = -int(math.Floor(component.random() * arminDisplayHeight * 2))
		}
	case arminFade:
		component.positions = component.shuffledPositions()
	case arminCRT:
		component.expansion = 0
	case arminGlitch:
		component.phase = 0
	case arminDissolve:
		characters := []rune{' ', '░', '▒', '▓', '█', '▀', '▄'}
		for row := range component.current {
			for column := range component.current[row] {
				index := int(math.Floor(component.random() * float64(len(characters))))
				index = max(0, min(len(characters)-1, index))
				component.current[row][column] = characters[index]
			}
		}
		component.positions = component.shuffledPositions()
	}
}

func (component *ArminComponent) shuffledPositions() []arminPosition {
	positions := make([]arminPosition, 0, arminWidth*arminDisplayHeight)
	for row := range arminDisplayHeight {
		for column := range arminWidth {
			positions = append(positions, arminPosition{row: row, column: column})
		}
	}
	for index := len(positions) - 1; index > 0; index-- {
		swap := int(math.Floor(component.random() * float64(index+1)))
		swap = max(0, min(index, swap))
		positions[index], positions[swap] = positions[swap], positions[index]
	}
	return positions
}

func (component *ArminComponent) tickEffect() bool {
	switch component.effect {
	case arminTypewriter:
		return component.tickTypewriter()
	case arminScanline:
		return component.tickScanline()
	case arminRain:
		return component.tickRain()
	case arminFade:
		return component.tickFade()
	case arminCRT:
		return component.tickCRT()
	case arminGlitch:
		return component.tickGlitch()
	case arminDissolve:
		return component.tickDissolve()
	default:
		return true
	}
}

func (component *ArminComponent) tickTypewriter() bool {
	for range 3 {
		row := component.position / arminWidth
		column := component.position % arminWidth
		if row >= arminDisplayHeight {
			return true
		}
		component.current[row][column] = component.final[row][column]
		component.position++
	}
	return false
}

func (component *ArminComponent) tickScanline() bool {
	if component.row >= arminDisplayHeight {
		return true
	}
	copy(component.current[component.row], component.final[component.row])
	component.row++
	return false
}

func (component *ArminComponent) tickRain() bool {
	allSettled := true
	component.current = emptyArminGrid()
	for column := range arminWidth {
		drop := &component.drops[column]
		for row := arminDisplayHeight - 1; row >= arminDisplayHeight-drop.settled; row-- {
			if row >= 0 {
				component.current[row][column] = component.final[row][column]
			}
		}
		if drop.settled >= arminDisplayHeight {
			continue
		}
		allSettled = false
		targetRow := -1
		for row := arminDisplayHeight - 1 - drop.settled; row >= 0; row-- {
			if component.final[row][column] != ' ' {
				targetRow = row
				break
			}
		}
		drop.y++
		if drop.y >= 0 && drop.y < arminDisplayHeight {
			if targetRow >= 0 && drop.y >= targetRow {
				drop.settled = arminDisplayHeight - targetRow
				drop.y = -int(math.Floor(component.random()*5)) - 1
			} else {
				component.current[drop.y][column] = '▓'
			}
		}
	}
	return allSettled
}

func (component *ArminComponent) tickFade() bool {
	for range 15 {
		if component.position >= len(component.positions) {
			return true
		}
		position := component.positions[component.position]
		component.current[position.row][position.column] = component.final[position.row][position.column]
		component.position++
	}
	return false
}

func (component *ArminComponent) tickCRT() bool {
	middle := arminDisplayHeight / 2
	component.current = emptyArminGrid()
	top, bottom := middle-component.expansion, middle+component.expansion
	for row := max(0, top); row <= min(arminDisplayHeight-1, bottom); row++ {
		copy(component.current[row], component.final[row])
	}
	component.expansion++
	return component.expansion > arminDisplayHeight
}

func (component *ArminComponent) tickGlitch() bool {
	if component.phase < 8 {
		current := make([][]rune, arminDisplayHeight)
		for row := range component.final {
			offset := int(math.Floor(component.random()*7)) - 3
			glitchRow := append([]rune(nil), component.final[row]...)
			if component.random() < 0.3 {
				current[row] = rotateArminRow(glitchRow, offset)
			} else if component.random() < 0.2 {
				swapRow := int(math.Floor(component.random() * arminDisplayHeight))
				swapRow = max(0, min(arminDisplayHeight-1, swapRow))
				current[row] = append([]rune(nil), component.final[swapRow]...)
			} else {
				current[row] = glitchRow
			}
		}
		component.current = current
		component.phase++
		return false
	}
	component.current = cloneArminGrid(component.final)
	return true
}

func (component *ArminComponent) tickDissolve() bool {
	for range 20 {
		if component.position >= len(component.positions) {
			return true
		}
		position := component.positions[component.position]
		component.current[position.row][position.column] = component.final[position.row][position.column]
		component.position++
	}
	return false
}

func buildArminGrid() [][]rune {
	grid := emptyArminGrid()
	for row := range arminDisplayHeight {
		for column := range arminWidth {
			upper := arminPixel(column, row*2)
			lower := arminPixel(column, row*2+1)
			switch {
			case upper && lower:
				grid[row][column] = '█'
			case upper:
				grid[row][column] = '▀'
			case lower:
				grid[row][column] = '▄'
			default:
				grid[row][column] = ' '
			}
		}
	}
	return grid
}

func arminPixel(column, row int) bool {
	if row >= arminHeight {
		return false
	}
	const bytesPerRow = (arminWidth + 7) / 8
	byteIndex := row*bytesPerRow + column/8
	bitIndex := column % 8
	return (arminBits[byteIndex]>>bitIndex)&1 == 0
}

func emptyArminGrid() [][]rune {
	grid := make([][]rune, arminDisplayHeight)
	for row := range grid {
		grid[row] = []rune(strings.Repeat(" ", arminWidth))
	}
	return grid
}

func cloneArminGrid(grid [][]rune) [][]rune {
	clone := make([][]rune, len(grid))
	for row := range grid {
		clone[row] = append([]rune(nil), grid[row]...)
	}
	return clone
}

func rotateArminRow(row []rune, offset int) []rune {
	start := offset
	if start < 0 {
		start = len(row) + start
	}
	if start < 0 || start >= len(row) {
		return append([]rune(nil), row...)
	}
	rotated := append([]rune(nil), row[start:]...)
	rotated = append(rotated, row[:start]...)
	return rotated
}
