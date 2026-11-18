package gox

// Content holds either plain text or explicitly trusted HTML. Its zero value is empty.
// TrustedHTML is a trust assertion, not a request to sanitize the content.
type Content struct {
	Content     string
	TrustedHTML bool
}

// Text constructs plain content, escaped for its rendering context.
func Text(value string) Content { return Content{Content: value} }

// TrustedHTML constructs content whose markup is trusted by the caller.
func TrustedHTML(value HTML) Content { return Content{Content: string(value), TrustedHTML: true} }
