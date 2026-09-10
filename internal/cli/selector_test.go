package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPickerModelSelectsAndCancels(t *testing.T) {
	t.Run("select", func(t *testing.T) {
		m := newPickerModel("Choose", []string{"first"})
		if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
			t.Fatal("enter did not return a quit command")
		}
		if m.selection != "first" || m.cancelled {
			t.Fatalf("selection = %q, cancelled = %v", m.selection, m.cancelled)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		m := newPickerModel("Choose", []string{"first"})
		if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd == nil {
			t.Fatal("escape did not return a quit command")
		}
		if !m.cancelled || m.selection != "" {
			t.Fatalf("selection = %q, cancelled = %v", m.selection, m.cancelled)
		}
	})
}
