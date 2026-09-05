package ui

type Model struct {
	Focused int
}

func (m *Model) Next(providerCount int) {
	if m == nil || providerCount <= 0 {
		return
	}
	m.Focused = (m.Focused + 1) % providerCount
}
