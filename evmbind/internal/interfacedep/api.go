package interfacedep

type Named interface{ Label() string }

type NamedValue struct{ Text string }

func (v *NamedValue) Label() string {
	if v == nil {
		return "nil"
	}
	return v.Text
}

func (v *NamedValue) Error() string { return v.Label() }
