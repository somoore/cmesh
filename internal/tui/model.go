package tui

import "bufio"

// action is the outcome of handling a single input byte.
type action int

const (
	// actionNone means the byte was consumed without a visible change.
	actionNone action = iota
	// actionRedraw means the model changed and the list should repaint.
	actionRedraw
	// actionConfirm means the user accepted the current selection (Enter).
	actionConfirm
	// actionCancel means the user cancelled (q / Ctrl-C / Esc).
	actionCancel
)

// selectModel is the pure state machine behind the interactive multi-select.
// It holds no terminal handles, so its navigation/toggle logic can be unit
// tested by feeding bytes and asserting on cursor/checked state.
type selectModel struct {
	items   []Item
	cursor  int
	painted bool
}

func newSelectModel(items []Item) *selectModel {
	cloned := make([]Item, len(items))
	copy(cloned, items)
	return &selectModel{items: cloned}
}

// handleByte advances the model for one input byte. A bufio.Reader is supplied
// so escape sequences (arrow keys: ESC '[' 'A'/'B') can read their trailing
// bytes. The reader may be nil in tests that only exercise single-byte keys.
func (m *selectModel) handleByte(b byte, reader *bufio.Reader) action {
	switch b {
	case 3, 4: // Ctrl-C, Ctrl-D
		return actionCancel
	case 'q', 'Q':
		return actionCancel
	case '\r', '\n':
		return actionConfirm
	case ' ':
		m.toggle()
		return actionRedraw
	case 'k', 'K':
		return m.moveUp()
	case 'j', 'J':
		return m.moveDown()
	case 0x1b: // ESC: possible arrow-key sequence "[A"/"[B"
		return m.handleEscape(reader)
	default:
		return actionNone
	}
}

func (m *selectModel) handleEscape(reader *bufio.Reader) action {
	if reader == nil {
		// A bare ESC with no follow-on bytes is treated as cancel.
		return actionCancel
	}
	b1, err := reader.ReadByte()
	if err != nil {
		return actionCancel
	}
	if b1 != '[' && b1 != 'O' {
		// Not a recognized CSI/SS3 sequence; treat the lone ESC as cancel.
		return actionCancel
	}
	b2, err := reader.ReadByte()
	if err != nil {
		return actionCancel
	}
	switch b2 {
	case 'A': // up arrow
		return m.moveUp()
	case 'B': // down arrow
		return m.moveDown()
	default:
		return actionNone
	}
}

func (m *selectModel) moveUp() action {
	if m.cursor > 0 {
		m.cursor--
		return actionRedraw
	}
	return actionNone
}

func (m *selectModel) moveDown() action {
	if m.cursor < len(m.items)-1 {
		m.cursor++
		return actionRedraw
	}
	return actionNone
}

func (m *selectModel) toggle() {
	if m.cursor >= 0 && m.cursor < len(m.items) {
		m.items[m.cursor].Checked = !m.items[m.cursor].Checked
	}
}

// selected returns the indices of checked items in original order.
func (m *selectModel) selected() []int {
	var out []int
	for i, it := range m.items {
		if it.Checked {
			out = append(out, i)
		}
	}
	return out
}
