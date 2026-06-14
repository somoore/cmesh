// Package tui provides small, hand-rolled terminal UI helpers built on
// golang.org/x/term. The flagship helper is MultiSelect, an arrow-key + space
// checkbox list used by the cmesh installer and uninstaller.
//
// Design rules:
//   - The raw-mode interactive path always restores the terminal on every exit
//     path (return, error, panic, cancel) via a defer registered immediately
//     after term.MakeRaw.
//   - When stdin/stdout is not a TTY (CI, pipes, redirected I/O) MultiSelect
//     degrades to a numbered prompt and NEVER enters raw mode.
//   - The interactive key-handling logic is factored into a pure model
//     (selectModel) so it can be unit-tested without a real terminal.
package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// ErrCancelled is returned when the user cancels the selection (q / Ctrl-C in
// raw mode, or an explicit cancel sentinel in the degraded numbered prompt).
var ErrCancelled = errors.New("selection cancelled")

// Item is a single selectable row.
type Item struct {
	// Label is the primary, human-readable text.
	Label string
	// Detail is optional secondary text shown beside the label.
	Detail string
	// Checked is the initial checkbox state.
	Checked bool
}

// MultiSelect presents items for selection and returns the indices of the
// checked items in their original order. in/out are the terminal files
// (typically os.Stdin/os.Stdout). When either is nil or not a TTY, it falls
// back to a numbered prompt that never touches raw mode.
//
// Returns ErrCancelled if the user cancels.
func MultiSelect(in, out *os.File, title string, items []Item) ([]int, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if isInteractive(in, out) {
		return multiSelectRaw(in, out, title, items)
	}
	return multiSelectNumbered(in, out, title, items)
}

func isInteractive(in, out *os.File) bool {
	return in != nil && out != nil &&
		term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

// multiSelectRaw drives the interactive arrow/space picker in terminal raw mode.
func multiSelectRaw(in, out *os.File, title string, items []Item) ([]int, error) {
	oldState, rawErr := term.MakeRaw(int(in.Fd()))
	if rawErr != nil {
		// Could not enter raw mode; fall back to the numbered prompt rather
		// than failing the whole install/uninstall flow.
		return multiSelectNumbered(in, out, title, items)
	}
	// Restore the terminal on EVERY exit path: normal return, error, and panic.
	defer func() {
		_ = term.Restore(int(in.Fd()), oldState)
	}()

	model := newSelectModel(items)
	reader := bufio.NewReader(in)
	render(out, title, model)
	buf := make([]byte, 1)
	for {
		n, readErr := reader.Read(buf)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil, ErrCancelled
			}
			return nil, readErr
		}
		if n == 0 {
			continue
		}
		switch model.handleByte(buf[0], reader) {
		case actionRedraw:
			render(out, title, model)
		case actionConfirm:
			fmt.Fprint(out, "\r\n")
			return model.selected(), nil
		case actionCancel:
			fmt.Fprint(out, "\r\n")
			return nil, ErrCancelled
		}
	}
}

// render draws the full list. It is called in raw mode so it uses \r\n and ANSI
// cursor-up to repaint in place after the first frame.
func render(out io.Writer, title string, m *selectModel) {
	var b strings.Builder
	if m.painted {
		// Move cursor up over the previously painted lines (title + items + hint).
		fmt.Fprintf(&b, "\x1b[%dA", len(m.items)+2)
	}
	b.WriteString("\r\x1b[J") // carriage return + clear to end of screen
	b.WriteString(title)
	b.WriteString("\r\n")
	for i, it := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		check := "[ ]"
		if it.Checked {
			check = "[x]"
		}
		line := fmt.Sprintf("%s%s %s", cursor, check, it.Label)
		if it.Detail != "" {
			line += "  " + it.Detail
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	b.WriteString("up/down move - space toggle - enter confirm - q cancel\r\n")
	io.WriteString(out, b.String())
	m.painted = true
}

// multiSelectNumbered is the non-TTY fallback. It never enters raw mode. The
// user enters a comma/space separated list of item numbers (1-based), an empty
// line accepts the defaults, and "q" cancels.
func multiSelectNumbered(in, out *os.File, title string, items []Item) ([]int, error) {
	var w io.Writer = out
	if w == nil {
		w = io.Discard
	}
	if in == nil {
		// No input at all: accept the defaults.
		return defaultSelected(items), nil
	}
	return multiSelectNumberedReader(in, w, title, items)
}

// multiSelectNumberedReader is the reader/writer core of the numbered fallback,
// factored out so it can be unit tested with a strings.Reader.
func multiSelectNumberedReader(in io.Reader, w io.Writer, title string, items []Item) ([]int, error) {
	fmt.Fprintln(w, title)
	for i, it := range items {
		mark := " "
		if it.Checked {
			mark = "*"
		}
		line := fmt.Sprintf("  [%d] (%s) %s", i+1, mark, it.Label)
		if it.Detail != "" {
			line += "  " + it.Detail
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprint(w, "Enter numbers to select (comma/space separated), blank for defaults, q to cancel: ")

	// Read one line WITHOUT read-ahead so any following bytes on a piped stdin
	// (e.g. a subsequent Y/N answer) survive for the caller's own reader. A
	// bufio.Reader would buffer past the newline and silently drop those bytes,
	// bypassing later confirmations.
	answer, err := readLineNoReadAhead(in)
	if err != nil && answer == "" {
		if errors.Is(err, io.EOF) {
			// No line available: accept defaults.
			return defaultSelected(items), nil
		}
		return nil, err
	}
	return parseNumberedSelection(answer, items)
}

// readLineNoReadAhead reads a single line from r one byte at a time, returning
// at (and including) the first newline. It never buffers beyond the newline.
func readLineNoReadAhead(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b.WriteByte(buf[0])
			if buf[0] == '\n' {
				return b.String(), nil
			}
		}
		if err != nil {
			return b.String(), err
		}
	}
}

// parseNumberedSelection interprets a numbered-prompt answer. An empty answer
// returns the default-checked items; "q"/"quit" returns ErrCancelled.
func parseNumberedSelection(answer string, items []Item) ([]int, error) {
	trimmed := strings.TrimSpace(answer)
	switch strings.ToLower(trimmed) {
	case "q", "quit":
		return nil, ErrCancelled
	case "":
		return defaultSelected(items), nil
	}
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	seen := make(map[int]bool, len(fields))
	var out []int
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(items) {
			return nil, fmt.Errorf("invalid selection %q", f)
		}
		idx := n - 1
		if !seen[idx] {
			seen[idx] = true
			out = append(out, idx)
		}
	}
	return out, nil
}

func defaultSelected(items []Item) []int {
	var out []int
	for i, it := range items {
		if it.Checked {
			out = append(out, i)
		}
	}
	return out
}
