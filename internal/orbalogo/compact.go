package orbalogo

const (
	// CompactWidth and CompactHeight are the update-screen canvas: the same
	// mark, about a third of the cells, so a one-shot CLI reveal does not fill
	// the terminal the way the empty-chat lockup does.
	CompactWidth  = 14
	CompactHeight = 6
)

// CompactFrame is Frame scaled onto the update canvas. The unfold stages stay
// the same indices; only the cell grid changes. The empty-chat lockup keeps
// the full mark; only the update reveal calls this.
func CompactFrame(index int) [CompactHeight]string { return compact(frames[index]) }

func compact(frame [Height]string) [CompactHeight]string {
	src := decode(frame[:], Width, Height)
	return encode(scale(src, CompactWidth*2, CompactHeight*4), CompactWidth, CompactHeight)
}

func decode(rows []string, width, height int) [][]bool {
	dots := make([][]bool, height*4)
	for y := range dots {
		dots[y] = make([]bool, width*2)
	}
	for row, line := range rows {
		column := 0
		for _, cell := range line {
			if cell >= 0x2800 && cell <= 0x28FF {
				value := int(cell - 0x2800)
				plot := func(bit, dx, dy int) {
					if value&(1<<bit) != 0 {
						dots[row*4+dy][column*2+dx] = true
					}
				}
				plot(0, 0, 0)
				plot(1, 0, 1)
				plot(2, 0, 2)
				plot(6, 0, 3)
				plot(3, 1, 0)
				plot(4, 1, 1)
				plot(5, 1, 2)
				plot(7, 1, 3)
			}
			column++
		}
	}
	return dots
}

func scale(src [][]bool, width, height int) [][]bool {
	srcHeight, srcWidth := len(src), len(src[0])
	dst := make([][]bool, height)
	for y := 0; y < height; y++ {
		dst[y] = make([]bool, width)
		y0, y1 := y*srcHeight/height, (y+1)*srcHeight/height
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < width; x++ {
			x0, x1 := x*srcWidth/width, (x+1)*srcWidth/width
			if x1 <= x0 {
				x1 = x0 + 1
			}
			on, count := 0, 0
			for yy := y0; yy < y1 && yy < srcHeight; yy++ {
				for xx := x0; xx < x1 && xx < srcWidth; xx++ {
					count++
					if src[yy][xx] {
						on++
					}
				}
			}
			dst[y][x] = on*2 >= count
		}
	}
	return dst
}

func encode(dots [][]bool, width, height int) [CompactHeight]string {
	var rows [CompactHeight]string
	lit := func(x, y int) bool {
		return y >= 0 && y < len(dots) && x >= 0 && x < len(dots[0]) && dots[y][x]
	}
	for row := 0; row < height; row++ {
		buf := make([]rune, 0, width)
		for column := 0; column < width; column++ {
			x, y := column*2, row*4
			value := 0
			if lit(x, y) {
				value |= 1
			}
			if lit(x, y+1) {
				value |= 2
			}
			if lit(x, y+2) {
				value |= 4
			}
			if lit(x+1, y) {
				value |= 8
			}
			if lit(x+1, y+1) {
				value |= 16
			}
			if lit(x+1, y+2) {
				value |= 32
			}
			if lit(x, y+3) {
				value |= 64
			}
			if lit(x+1, y+3) {
				value |= 128
			}
			if value == 0 {
				buf = append(buf, ' ')
			} else {
				buf = append(buf, rune(0x2800+value))
			}
		}
		for len(buf) > 0 && buf[len(buf)-1] == ' ' {
			buf = buf[:len(buf)-1]
		}
		rows[row] = string(buf)
	}
	return rows
}
