package omittedfixture

type Page struct {
	Title   string
	Private any
	Count   int
}

func Get() Page             { panic("host") }
func Pointer() *Page        { panic("host") }
func Check(page *Page) bool { panic("host") }
