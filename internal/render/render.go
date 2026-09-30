// Package render is teamscrawl's terminal presentation layer: a pixel-block
// wordmark, doctor screens, aligned tables and generic key/value blocks.
//
// It is adapted from slacrawl's internal/cli/render.go
// (https://github.com/vincentkoc/slacrawl, MIT License,
// Copyright (c) the slacrawl authors). The banner, the section underline
// style, the check glyphs, the table layout and the key/value block renderer
// follow that file; the wordmark, the Teams palette and the width handling
// are teamscrawl's own.
package render

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/mattn/go-runewidth"
)

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiBold   = "\x1b[1m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
)

// Status is the outcome of one doctor check.
type Status int

const (
	OK Status = iota
	Warn
	Fail
)

// Check is one doctor row. Fix is shown beneath warning and failing checks.
type Check struct {
	Name, Detail, Fix string
	Status            Status
}

// Snapshot is the generic summary section of a doctor screen. Pairs render
// on one "|"-separated line as key=value; Lines render as aligned rows.
type Snapshot struct {
	Pairs [][2]string
	Lines [][2]string
}

// cond pins ambiguous-width glyphs to one column so output does not depend
// on the reader's locale.
var cond = func() *runewidth.Condition {
	c := runewidth.NewCondition()
	c.EastAsianWidth = false
	return c
}()

func displayWidth(s string) int { return cond.StringWidth(s) }

func padRight(s string, width int) string {
	if pad := width - displayWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func colorize(on bool, code, s string) string {
	if !on || s == "" || code == "" {
		return s
	}
	return code + s + ansiReset
}

func truecolor() bool {
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return true
	}
	return false
}

// purple is Teams purple (#6264A7); lavender is a lighter accent (#B4B6E4).
func purple() string {
	if truecolor() {
		return "\x1b[38;2;98;100;167m"
	}
	return "\x1b[38;5;61m"
}

func lavender() string {
	if truecolor() {
		return "\x1b[38;2;180;182;228m"
	}
	return "\x1b[38;5;146m"
}

// wordmark holds one glyph per letter of "teamscrawl", five rows each.
var wordmark = [][5]string{
	{" ▄▄ ", " ██ ", "▀██▀", " ██ ", " ▀█▄"},                // t
	{"     ", "     ", "▄███▄", "██▀▀▀", "▀████"},           // e
	{"     ", "     ", " ▀▀█▄", "▄█▀██", "▀█▄██"},           // a
	{"       ", "       ", "██▀█▀██", "██ █ ██", "██   ██"}, // m
	{"     ", "     ", "▄█▀▀▀", "▀███▄", "▄▄▄█▀"},           // s
	{"     ", "     ", "▄████", "██    ", "▀████"},          // c
	{"     ", "     ", "████▄", "██ ▀▀", "██   "},           // r
	{"     ", "     ", " ▀▀█▄", "▄█▀██", "▀█▄██"},           // a
	{"       ", "       ", "██   ██", "██ █ ██", " ██▀██ "}, // w
	{"▄▄", "██", "██", "██", "██"},                          // l
}

func init() {
	for i := range wordmark {
		w := 0
		for _, row := range wordmark[i] {
			if n := displayWidth(row); n > w {
				w = n
			}
		}
		for r := range wordmark[i] {
			wordmark[i][r] = padRight(wordmark[i][r], w)
		}
	}
}

// Banner writes the wordmark, a dim subtitle and a blank line.
func Banner(w io.Writer, subtitle string, color bool) {
	var b strings.Builder
	for r := 0; r < 5; r++ {
		for i, glyph := range wordmark {
			if i > 0 {
				b.WriteByte(' ')
			}
			accent := purple()
			if i == 0 {
				accent = lavender()
			}
			b.WriteString(colorize(color, accent, strings.TrimRight(glyph[r], " ")))
			b.WriteString(strings.Repeat(" ", displayWidth(glyph[r])-displayWidth(strings.TrimRight(glyph[r], " "))))
		}
		b.WriteString("\n")
	}
	b.WriteString(colorize(color, ansiDim, "local-first Teams mirror for SQLite"))
	if subtitle != "" {
		b.WriteString(colorize(color, ansiDim, "  |  "))
		b.WriteString(colorize(color, ansiCyan, strings.ToLower(subtitle)))
	}
	b.WriteString("\n\n")
	_, _ = io.WriteString(w, trimLineEnds(b.String()))
}

// trimLineEnds removes trailing spaces from plain lines (colored lines end in
// a reset code, so only bare padding is touched).
func trimLineEnds(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

func title(b *strings.Builder, color bool, text string) {
	b.WriteString(colorize(color, ansiBold+ansiCyan, strings.ToUpper(text)))
	b.WriteByte('\n')
	b.WriteString(colorize(color, ansiDim, strings.Repeat("=", displayWidth(text))))
	b.WriteString("\n\n")
}

func group(b *strings.Builder, color bool, text string, depth int) {
	b.WriteString(indent(depth))
	b.WriteString(colorize(color, ansiCyan, text))
	b.WriteByte('\n')
	b.WriteString(indent(depth))
	b.WriteString(colorize(color, ansiDim, strings.Repeat("-", displayWidth(text))))
	b.WriteByte('\n')
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

func statusGlyph(s Status) (string, string) {
	switch s {
	case Warn:
		return "▲", ansiYellow
	case Fail:
		return "✖", ansiRed
	default:
		return "●", ansiGreen
	}
}

// Doctor writes a titled "Ready checks" group followed by the Snapshot.
func Doctor(w io.Writer, heading string, checks []Check, snap *Snapshot, color bool) {
	var b strings.Builder
	if heading != "" {
		title(&b, color, heading)
	}
	group(&b, color, "Ready checks", 0)
	nameWidth := 16
	for _, c := range checks {
		if n := displayWidth(c.Name); n > nameWidth {
			nameWidth = n
		}
	}
	for _, c := range checks {
		glyph, code := statusGlyph(c.Status)
		detail := c.Detail
		if detail == "" {
			detail = "-"
		}
		b.WriteString("  " + colorize(color, code, glyph) + " ")
		b.WriteString(colorize(color, ansiDim, padRight(c.Name, nameWidth)))
		b.WriteString("  " + detail + "\n")
		if c.Fix != "" && c.Status != OK {
			b.WriteString(strings.Repeat(" ", 4+nameWidth+2))
			b.WriteString(colorize(color, code, "fix: "+c.Fix))
			b.WriteByte('\n')
		}
	}
	if snap != nil && (len(snap.Pairs) > 0 || len(snap.Lines) > 0) {
		b.WriteByte('\n')
		group(&b, color, "Snapshot", 0)
		if len(snap.Pairs) > 0 {
			b.WriteString("  ")
			for i, p := range snap.Pairs {
				if i > 0 {
					b.WriteString(colorize(color, ansiDim, "  |  "))
				}
				b.WriteString(colorize(color, ansiDim, p[0]+"="))
				b.WriteString(p[1])
			}
			b.WriteByte('\n')
		}
		labelWidth := 0
		for _, l := range snap.Lines {
			if n := displayWidth(l[0]); n > labelWidth {
				labelWidth = n
			}
		}
		for _, l := range snap.Lines {
			b.WriteString("  " + colorize(color, ansiDim, padRight(l[0], labelWidth)) + "  " + cell(l[1]) + "\n")
		}
	}
	_, _ = io.WriteString(w, b.String())
}

func cell(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Table writes aligned columns measured by display width, with a dim header
// and a dashed rule.
func Table(w io.Writer, columns []string, rows [][]string, color bool) {
	var b strings.Builder
	writeTable(&b, columns, rows, color, 0)
	_, _ = io.WriteString(w, b.String())
}

func writeTable(b *strings.Builder, columns []string, rows [][]string, color bool, depth int) {
	widths := make([]int, len(columns))
	for i, c := range columns {
		widths[i] = displayWidth(c)
	}
	for _, row := range rows {
		for i := range columns {
			if i < len(row) {
				if n := displayWidth(cell(row[i])); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	line := func(cells func(i int) (string, string)) {
		b.WriteString(indent(depth))
		for i := range columns {
			if i > 0 {
				b.WriteString("  ")
			}
			text, code := cells(i)
			if i == len(columns)-1 {
				b.WriteString(colorize(color, code, text))
				continue
			}
			b.WriteString(colorize(color, code, text))
			b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(text)))
		}
		b.WriteByte('\n')
	}
	line(func(i int) (string, string) { return columns[i], ansiDim })
	line(func(i int) (string, string) { return strings.Repeat("-", widths[i]), ansiDim })
	if len(rows) == 0 {
		b.WriteString(indent(depth) + "no rows\n")
		return
	}
	for _, row := range rows {
		line(func(i int) (string, string) {
			if i >= len(row) || row[i] == "" {
				return "-", ansiDim
			}
			return row[i], ""
		})
	}
}

// Block renders a map, struct, slice or scalar as a titled key/value block.
func Block(w io.Writer, heading string, value any, color bool) {
	var b strings.Builder
	if heading != "" {
		title(&b, color, heading)
	}
	renderValue(&b, normalize(value), 0, color)
	_, _ = io.WriteString(w, b.String())
}

func renderValue(b *strings.Builder, value any, depth int, color bool) {
	switch v := value.(type) {
	case map[string]any:
		renderMap(b, v, depth, color)
	case []any:
		renderSlice(b, v, depth, color)
	default:
		b.WriteString(indent(depth) + formatScalar(v, color) + "\n")
	}
}

func renderMap(b *strings.Builder, m map[string]any, depth int, color bool) {
	if len(m) == 0 {
		b.WriteString(indent(depth) + "(empty)\n")
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var scalars, nested []string
	maxLabel := 0
	for _, k := range keys {
		if isScalar(m[k]) {
			scalars = append(scalars, k)
			if n := displayWidth(humanize(k)); n > maxLabel {
				maxLabel = n
			}
		} else {
			nested = append(nested, k)
		}
	}
	for _, k := range scalars {
		b.WriteString(indent(depth))
		b.WriteString(colorize(color, ansiDim, padRight(humanize(k), maxLabel)))
		b.WriteString(colorize(color, ansiDim, " : "))
		b.WriteString(formatScalar(m[k], color) + "\n")
	}
	for i, k := range nested {
		if i > 0 || len(scalars) > 0 {
			b.WriteByte('\n')
		}
		group(b, color, humanize(k), depth)
		renderValue(b, m[k], depth+1, color)
	}
}

func renderSlice(b *strings.Builder, s []any, depth int, color bool) {
	if len(s) == 0 {
		b.WriteString(indent(depth) + "no rows\n")
		return
	}
	var rows []map[string]any
	allMaps := true
	for _, item := range s {
		if m, ok := item.(map[string]any); ok {
			rows = append(rows, m)
		} else {
			allMaps = false
		}
	}
	if !allMaps {
		for _, item := range s {
			if isScalar(item) {
				b.WriteString(indent(depth) + "- " + formatScalar(item, color) + "\n")
			} else {
				b.WriteString(indent(depth) + "-\n")
				renderValue(b, item, depth+1, color)
			}
		}
		return
	}
	seen := map[string]bool{}
	var keys []string
	for _, r := range rows {
		var rk []string
		for k := range r {
			rk = append(rk, k)
		}
		sort.Strings(rk)
		for _, k := range rk {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	header := make([]string, len(keys))
	for i, k := range keys {
		header[i] = humanize(k)
	}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		for _, k := range keys {
			cells[i] = append(cells[i], plainScalar(r[k]))
		}
	}
	writeTable(b, header, cells, color, depth)
}

func humanize(s string) string {
	return strings.NewReplacer("_", " ", "-", " ").Replace(s)
}

func isScalar(v any) bool {
	switch v.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, time.Time:
		return true
	}
	return false
}

func plainScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case time.Time:
		if t.IsZero() {
			return "never"
		}
		return t.Format(time.RFC3339)
	case string:
		return cell(t)
	default:
		return fmt.Sprint(t)
	}
}

func formatScalar(v any, color bool) string {
	plain := plainScalar(v)
	switch t := v.(type) {
	case nil:
		return colorize(color, ansiDim, plain)
	case bool:
		if t {
			return colorize(color, ansiGreen, plain)
		}
		return colorize(color, ansiYellow, plain)
	case time.Time:
		if t.IsZero() {
			return colorize(color, ansiDim, plain)
		}
	case string:
		if t == "" {
			return colorize(color, ansiDim, plain)
		}
	}
	return plain
}

// normalize converts structs, pointers, maps and slices to map[string]any and
// []any, keyed by json tag names.
func normalize(value any) any {
	if value == nil || isScalar(value) {
		return value
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if isScalar(rv.Interface()) {
		return rv.Interface()
	}
	switch rv.Kind() {
	case reflect.Struct:
		out := map[string]any{}
		rt := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			key := strings.Split(f.Tag.Get("json"), ",")[0]
			if key == "-" {
				continue
			}
			if key == "" {
				key = f.Name
			}
			out[key] = normalize(rv.Field(i).Interface())
		}
		return out
	case reflect.Map:
		out := map[string]any{}
		for it := rv.MapRange(); it.Next(); {
			out[fmt.Sprint(it.Key().Interface())] = normalize(it.Value().Interface())
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, normalize(rv.Index(i).Interface()))
		}
		return out
	default:
		return fmt.Sprint(value)
	}
}

// ColorEnabled reports whether f should receive ANSI color: f is a terminal,
// NO_COLOR is unset or empty, and the --no-color flag is off.
func ColorEnabled(f *os.File, noColorFlag bool) bool {
	tty := f != nil && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
	return colorEnabled(tty, noColorFlag, os.LookupEnv)
}

func colorEnabled(tty, noColorFlag bool, lookup func(string) (string, bool)) bool {
	if !tty || noColorFlag {
		return false
	}
	if v, ok := lookup("NO_COLOR"); ok && v != "" {
		return false
	}
	return true
}

// Dim wraps s in the dim style when color is on.
func Dim(s string, color bool) string { return colorize(color, ansiDim, s) }

// Truncate clips s to at most width display columns, ending in "..." when it
// had to cut. Width is measured like Table measures it, so CJK and emoji are
// neither split nor counted as one column.
func Truncate(s string, width int) string {
	if width <= 0 || displayWidth(s) <= width {
		return s
	}
	const ellipsis = "..."
	if width <= len(ellipsis) {
		return cond.Truncate(s, width, "")
	}
	return cond.Truncate(s, width, ellipsis)
}
