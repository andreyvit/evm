package emptydata

type SetHolder struct {
	Set    map[string]struct{}
	Items  []struct{}
	Marker *struct{}
}

func (h *SetHolder) Has(key string) bool { _, ok := h.Set[key]; return ok }

type Consumer struct {
	Set    map[string]struct{}
	Items  []struct{}
	Marker *struct{}
}
