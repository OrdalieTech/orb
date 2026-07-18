package tui

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

func (markdown *Markdown) renderTable(table *extast.Table, source []byte, availableWidth int, style *inlineStyleContext) []string {
	header, rows := tableRows(table)
	columns := len(header)
	if columns == 0 {
		return nil
	}
	borderOverhead := 3*columns + 1
	availableForCells := availableWidth - borderOverhead
	if availableForCells < columns {
		return WrapTextWithANSI(string(blockText(table, source)), availableWidth)
	}

	context := markdown.resolveInlineContext(style)
	natural := make([]int, columns)
	minimum := make([]int, columns)
	cellText := func(cell *extast.TableCell) string {
		return markdown.renderInlineChildren(cell, source, context)
	}
	for index, cell := range header {
		value := cellText(cell)
		natural[index] = VisibleWidth(value)
		minimum[index] = max(1, longestWordWidth(value, 30))
	}
	for _, row := range rows {
		for index, cell := range row {
			if index >= columns {
				break
			}
			value := cellText(cell)
			natural[index] = max(natural[index], VisibleWidth(value))
			minimum[index] = max(minimum[index], max(1, longestWordWidth(value, 30)))
		}
	}

	minimumTotal := intSum(minimum)
	if minimumTotal > availableForCells {
		weights := append([]int(nil), minimum...)
		minimum = make([]int, columns)
		for index := range minimum {
			minimum[index] = 1
		}
		remaining := availableForCells - columns
		if remaining > 0 {
			totalWeight := 0
			for _, value := range weights {
				totalWeight += max(0, min(value, 30)-1)
			}
			allocated := 0
			if totalWeight > 0 {
				for index, value := range weights {
					growth := max(0, min(value, 30)-1) * remaining / totalWeight
					minimum[index] += growth
					allocated += growth
				}
			}
			for leftover, index := remaining-allocated, 0; leftover > 0; leftover, index = leftover-1, index+1 {
				minimum[index%columns]++
			}
		}
		minimumTotal = intSum(minimum)
	}

	widths := make([]int, columns)
	if intSum(natural)+borderOverhead <= availableWidth {
		for index := range widths {
			widths[index] = max(natural[index], minimum[index])
		}
	} else {
		growPotential := 0
		for index, value := range natural {
			growPotential += max(0, value-minimum[index])
		}
		extra := max(0, availableForCells-minimumTotal)
		for index := range widths {
			growth := 0
			if growPotential > 0 {
				growth = max(0, natural[index]-minimum[index]) * extra / growPotential
			}
			widths[index] = minimum[index] + growth
		}
		remaining := availableForCells - intSum(widths)
		for remaining > 0 {
			grew := false
			for index := 0; index < columns && remaining > 0; index++ {
				if widths[index] < natural[index] {
					widths[index]++
					remaining--
					grew = true
				}
			}
			if !grew {
				break
			}
		}
	}

	lines := []string{"┌─" + repeatWidths(widths, "─┬─") + "─┐"}
	lines = append(lines, markdown.renderTableRow(header, source, widths, true, context)...)
	separator := "├─" + repeatWidths(widths, "─┼─") + "─┤"
	lines = append(lines, separator)
	for index, row := range rows {
		lines = append(lines, markdown.renderTableRow(row, source, widths, false, context)...)
		if index < len(rows)-1 {
			lines = append(lines, separator)
		}
	}
	lines = append(lines, "└─"+repeatWidths(widths, "─┴─")+"─┘")
	return lines
}

func tableRows(table *extast.Table) ([]*extast.TableCell, [][]*extast.TableCell) {
	var header []*extast.TableCell
	rows := make([][]*extast.TableCell, 0)
	for child := table.FirstChild(); child != nil; child = child.NextSibling() {
		cells := make([]*extast.TableCell, 0)
		for cell := child.FirstChild(); cell != nil; cell = cell.NextSibling() {
			if typed, ok := cell.(*extast.TableCell); ok {
				cells = append(cells, typed)
			}
		}
		if _, ok := child.(*extast.TableHeader); ok {
			header = cells
		} else {
			rows = append(rows, cells)
		}
	}
	return header, rows
}

func (markdown *Markdown) renderTableRow(cells []*extast.TableCell, source []byte, widths []int, header bool, context inlineStyleContext) []string {
	wrapped := make([][]string, len(widths))
	height := 1
	for index := range widths {
		value := ""
		if index < len(cells) {
			value = markdown.renderInlineChildren(cells[index], source, context)
		}
		wrapped[index] = WrapTextWithANSI(value, max(1, widths[index]))
		height = max(height, len(wrapped[index]))
	}
	lines := make([]string, 0, height)
	for lineIndex := 0; lineIndex < height; lineIndex++ {
		parts := make([]string, len(widths))
		for column := range widths {
			value := ""
			if lineIndex < len(wrapped[column]) {
				value = wrapped[column][lineIndex]
			}
			value += strings.Repeat(" ", max(0, widths[column]-VisibleWidth(value)))
			if header {
				value = markdown.theme.Bold(value)
			}
			parts[column] = value
		}
		lines = append(lines, "│ "+strings.Join(parts, " │ ")+" │")
	}
	return lines
}

func longestWordWidth(value string, limit int) int {
	longest := 0
	for _, word := range strings.Fields(value) {
		longest = max(longest, VisibleWidth(word))
	}
	return min(longest, limit)
}

func intSum(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func repeatWidths(widths []int, separator string) string {
	parts := make([]string, len(widths))
	for index, width := range widths {
		parts[index] = strings.Repeat("─", width)
	}
	return strings.Join(parts, separator)
}

var _ ast.Node = (*extast.Table)(nil)
