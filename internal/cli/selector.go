package cli

import (
	"fmt"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
)

type pickerModel struct {
	list      list.Model
	selection string
	cancelled bool
}

func newPickerModel(title string, values []string) *pickerModel {
	items := make([]list.Item, len(values))
	for i, value := range values {
		items[i] = selectorItem(value)
	}
	l := list.New(items, list.NewDefaultDelegate(), 80, 24)
	l.Title = title
	return &pickerModel{list: l}
}

func (m *pickerModel) Init() tea.Cmd { return nil }

func (m *pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			if item, ok := m.list.SelectedItem().(selectorItem); ok {
				m.selection = string(item)
				return m, tea.Quit
			}
		case "esc", "ctrl+c", "ctrl+d":
			m.cancelled = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *pickerModel) View() tea.View {
	var view tea.View
	view.SetContent(m.list.View())
	return view
}

func runPicker(cmd *cobra.Command, title string, values []string) (string, error) {
	if len(values) == 0 {
		return "", fmt.Errorf("nothing to select")
	}
	m := newPickerModel(title, values)
	program := tea.NewProgram(m, tea.WithContext(cmd.Context()), tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()))
	if _, err := program.Run(); err != nil {
		return "", err
	}
	if m.cancelled || m.selection == "" {
		return "", fmt.Errorf("selection cancelled")
	}
	return m.selection, nil
}

func pickProviderModel(cmd *cobra.Command) (string, error) {
	names, err := configuredProviderNames()
	if err != nil {
		return "", err
	}
	name, err := runPicker(cmd, "Choose a provider", names)
	if err != nil {
		return "", err
	}
	models, err := providerModels(cmd.Context(), name)
	if err != nil {
		return "", err
	}
	model, err := runPicker(cmd, "Choose a model for "+name, models)
	if err != nil {
		return "", err
	}
	return name + "/" + model, nil
}
