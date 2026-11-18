package collectionfixture

type Item struct{ Title string }
type Panel struct {
	Metadata map[string]any
	Nested   []map[string]any
	Payload  any
	Values   []any
	Tags     []string
	Groups   [][]string
	Items    []*Item
	Named    map[string]*Item
}

func Roundtrip(p Panel) Panel { panic("host") }

func GetAny() any { panic("host") }
