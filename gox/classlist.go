package gox

import "strings"

// ClassList builds a space-separated class attribute for passing to another
// renderer. Prefer Builder.Class when styling the current element directly.
// The zero value is ready to use. Do not copy a ClassList after the first Add.
type ClassList struct {
	value strings.Builder
}

// Add appends a nonempty class group, separated by a single space.
// It trims surrounding whitespace without changing whitespace within the group.
func (c *ClassList) Add(group string) {
	group = strings.TrimSpace(group)
	if group == "" {
		return
	}
	if c.value.Len() != 0 {
		c.value.WriteByte(' ')
	}
	c.value.WriteString(group)
}

// String returns the accumulated classes. Later Add calls leave it unchanged.
func (c *ClassList) String() string { return c.value.String() }
