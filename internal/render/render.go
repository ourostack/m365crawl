// Package render is m365crawl's terminal presentation layer: a pixel-block
// wordmark, doctor screens, aligned tables and generic key/value blocks.
//
// It is adapted from slacrawl's internal/cli/render.go
// (https://github.com/vincentkoc/slacrawl, MIT License,
// Copyright (c) the slacrawl authors). The banner, the section underline
// style, the check glyphs, the table layout and the key/value block renderer
// follow that file; the wordmark, the Copilot gradient and the width handling
// are m365crawl's own.
package render

import (
	"fmt"
	"io"
	"math"
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

// gradientStops are the Microsoft 365 / Copilot wordmark colours, left to
// right: blue, indigo, purple, pink, orange.
var gradientStops = [5][3]int{
	{0x2E, 0xA7, 0xE8},
	{0x4F, 0x6B, 0xED},
	{0x8A, 0x5C, 0xF6},
	{0xD7, 0x42, 0x9E},
	{0xF9, 0x8B, 0x43},
}

// gradientAt interpolates the stops linearly; t is clamped to [0, 1].
func gradientAt(t float64) [3]int {
	t = math.Max(0, math.Min(1, t))
	n := len(gradientStops) - 1
	i := int(t * float64(n))
	if i > n-1 {
		i = n - 1
	}
	// The explicit conversions stop arm64 from fusing multiply-add, so every
	// platform rounds identically.
	f := float64(t*float64(n)) - float64(i)
	var out [3]int
	for k := range out {
		a, b := float64(gradientStops[i][k]), float64(gradientStops[i+1][k])
		out[k] = int(math.RoundToEven(a + float64((b-a)*f)))
	}
	return out
}

// cubeLevels are the xterm-256 colour cube channel values (indexes 16-231).
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

func sqDist(a, b [3]int) int {
	d := 0
	for k := range a {
		d += (a[k] - b[k]) * (a[k] - b[k])
	}
	return d
}

// xterm256 returns the xterm-256 index nearest to c, searching the colour
// cube (16-231) and the grey ramp (232-255). The 16 theme-defined system
// colours are skipped because their appearance is up to the terminal.
func xterm256(c [3]int) int {
	best, bestD := 16, -1
	for i := 16; i < 256; i++ {
		var p [3]int
		if i < 232 {
			n := i - 16
			p = [3]int{cubeLevels[n/36], cubeLevels[(n/6)%6], cubeLevels[n%6]}
		} else {
			g := 8 + 10*(i-232)
			p = [3]int{g, g, g}
		}
		if d := sqDist(c, p); bestD < 0 || d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// gradientCode is the foreground escape for column col of a width-wide
// wordmark.
func gradientCode(col, width int) string {
	t := 0.0
	if width > 1 {
		t = float64(col) / float64(width-1)
	}
	c := gradientAt(t)
	if truecolor() {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
	}
	return fmt.Sprintf("\x1b[38;5;%dm", xterm256(c))
}

// wordmark holds one glyph per letter of "m365crawl", five rows each.
var wordmark = [][5]string{
	{"       ", "       ", "██▀█▀██", "██ █ ██", "██   ██"}, // m
	{"▄████▄", "    ██", "  ███▀", "    ██", "▀████▀"},      // 3
	{"▄████▄", "██    ", "█████▄", "██  ██", "▀████▀"},      // 6
	{"██████", "██    ", "█████▄", "    ██", "▀████▀"},      // 5
	{"     ", "     ", "▄████", "██   ", "▀████"},           // c
	{"     ", "     ", "████▄", "██ ▀▀", "██   "},           // r
	{"     ", "     ", " ▀▀█▄", "▄█▀██", "▀█▄██"},           // a
	{"       ", "       ", "██   ██", "██ █ ██", " ██▀██ "}, // w
	{"▄▄", "██", "██", "██", "██"},                          // l
}

// wordmarkRows joins the glyphs with one space between letters.
func wordmarkRows() [5]string {
	var rows [5]string
	for r := range rows {
		parts := make([]string, len(wordmark))
		for i, g := range wordmark {
			parts[i] = g[r]
		}
		rows[r] = strings.Join(parts, " ")
	}
	return rows
}

// Banner writes the wordmark, a dim subtitle and a blank line. With colour on,
// each column takes its colour from the gradient across the wordmark width.
func Banner(w io.Writer, subtitle string, color bool) {
	var b strings.Builder
	rows := wordmarkRows()
	width := displayWidth(rows[0])
	for _, row := range rows {
		for i, r := range []rune(row) {
			if r == ' ' || !color {
				b.WriteRune(r)
				continue
			}
			b.WriteString(gradientCode(i, width))
			b.WriteRune(r)
		}
		if color {
			b.WriteString(ansiReset)
		}
		b.WriteByte('\n')
	}
	text := "local-first Microsoft 365 mirror for SQLite"
	if subtitle != "" {
		b.WriteString(colorize(color, ansiDim, text+"  |  "))
		b.WriteString(colorize(color, ansiCyan, strings.ToLower(subtitle)))
	} else {
		b.WriteString(colorize(color, ansiDim, text))
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
