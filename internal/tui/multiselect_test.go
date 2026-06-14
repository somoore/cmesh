package tui

import (
	"bufio"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestParseNumberedSelection(t *testing.T) {
	items := []Item{
		{Label: "a", Checked: true},
		{Label: "b", Checked: true},
		{Label: "c"},
	}
	tests := []struct {
		name       string
		answer     string
		want       []int
		wantCancel bool
		wantErr    bool
	}{
		{name: "blank accepts defaults", answer: "\n", want: []int{0, 1}},
		{name: "blank with spaces accepts defaults", answer: "   \n", want: []int{0, 1}},
		{name: "single number", answer: "3\n", want: []int{2}},
		{name: "comma separated", answer: "1,3\n", want: []int{0, 2}},
		{name: "space separated", answer: "1 2 3\n", want: []int{0, 1, 2}},
		{name: "mixed separators dedup", answer: "2, 2 3\n", want: []int{1, 2}},
		{name: "q cancels", answer: "q\n", wantCancel: true},
		{name: "quit cancels", answer: "QUIT\n", wantCancel: true},
		{name: "out of range", answer: "9\n", wantErr: true},
		{name: "non-numeric", answer: "x\n", wantErr: true},
		{name: "zero rejected", answer: "0\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNumberedSelection(tt.answer, items)
			switch {
			case tt.wantCancel:
				if !errors.Is(err, ErrCancelled) {
					t.Fatalf("err = %v, want ErrCancelled", err)
				}
			case tt.wantErr:
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestMultiSelectNumberedDegrade(t *testing.T) {
	// in/out nil simulates a non-TTY with no usable input: defaults are returned
	// and MakeRaw is never called (it would panic on a nil file).
	items := []Item{{Label: "Claude Code", Checked: true}, {Label: "Codex"}}
	got, err := MultiSelect(nil, nil, "Pick agents", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("got %v, want [0]", got)
	}
}

func TestMultiSelectNumberedReader(t *testing.T) {
	// Drive the numbered fallback via a strings.Reader on stdin-like input by
	// exercising the parser path through multiSelectNumbered with a real reader.
	items := []Item{{Label: "a", Checked: true}, {Label: "b", Checked: true}}
	var out strings.Builder
	got, err := multiSelectNumberedReader(strings.NewReader("1\n"), &out, "title", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("got %v, want [0]", got)
	}
	if !strings.Contains(out.String(), "title") {
		t.Fatalf("title not rendered: %q", out.String())
	}
}

func TestMultiSelectNumberedNoReadAhead(t *testing.T) {
	// The numbered degrade path must NOT consume bytes past its own newline, so
	// a following Y/N answer on the same piped stdin survives for the caller.
	items := []Item{{Label: "a", Checked: true}, {Label: "b", Checked: true}}
	reader := strings.NewReader("1\nn\n")
	var out strings.Builder
	got, err := multiSelectNumberedReader(reader, &out, "title", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("got %v, want [0]", got)
	}
	// The remaining "n\n" must still be readable from the same reader.
	rest, _ := io.ReadAll(reader)
	if string(rest) != "n\n" {
		t.Fatalf("leftover bytes = %q, want %q", string(rest), "n\n")
	}
}

func TestModelNavigationAndToggle(t *testing.T) {
	items := []Item{{Label: "a", Checked: true}, {Label: "b"}, {Label: "c"}}
	m := newSelectModel(items)

	// Initial defaults.
	if got := m.selected(); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("initial selected = %v, want [0]", got)
	}
	// Move down twice (j), then toggle (space) item c on.
	if act := m.handleByte('j', nil); act != actionRedraw {
		t.Fatalf("j action = %v", act)
	}
	if act := m.handleByte('j', nil); act != actionRedraw {
		t.Fatalf("j action = %v", act)
	}
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.cursor)
	}
	// Move past the end is a no-op.
	if act := m.handleByte('j', nil); act != actionNone {
		t.Fatalf("j past end action = %v, want none", act)
	}
	m.handleByte(' ', nil) // toggle c on
	if got := m.selected(); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("after toggle selected = %v, want [0 2]", got)
	}
	// Toggle item a (cursor up twice) off.
	m.handleByte('k', nil)
	m.handleByte('k', nil)
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	m.handleByte(' ', nil)
	if got := m.selected(); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("after second toggle selected = %v, want [2]", got)
	}
}

func TestModelArrowKeys(t *testing.T) {
	items := []Item{{Label: "a"}, {Label: "b"}}
	m := newSelectModel(items)
	// ESC [ B is down arrow.
	r := bufio.NewReader(strings.NewReader("[B"))
	if act := m.handleByte(0x1b, r); act != actionRedraw {
		t.Fatalf("down arrow action = %v", act)
	}
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	// ESC [ A is up arrow.
	r = bufio.NewReader(strings.NewReader("[A"))
	if act := m.handleByte(0x1b, r); act != actionRedraw {
		t.Fatalf("up arrow action = %v", act)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
}

func TestModelConfirmAndCancel(t *testing.T) {
	m := newSelectModel([]Item{{Label: "a"}})
	if act := m.handleByte('\r', nil); act != actionConfirm {
		t.Fatalf("enter action = %v, want confirm", act)
	}
	if act := m.handleByte('q', nil); act != actionCancel {
		t.Fatalf("q action = %v, want cancel", act)
	}
	if act := m.handleByte(3, nil); act != actionCancel {
		t.Fatalf("ctrl-c action = %v, want cancel", act)
	}
}
