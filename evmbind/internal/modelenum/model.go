package modelenum

import "embed"

//go:embed model.go
var Source embed.FS

type Kind int

const Ready Kind = 1

func (k Kind) String() string {
	if k == Ready {
		return "ready"
	}
	return "unknown"
}

type Row struct {
	Kind Kind `kvo:"kind"`
}

func Current() *Row { return &Row{Kind: Ready} }
