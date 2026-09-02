// Package emailcontent prepares provider email content for safe in-app rendering.
package emailcontent

import "github.com/microcosm-cc/bluemonday"

var policy = newPolicy()

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("a", "b", "blockquote", "br", "caption", "code", "div", "em", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "li", "ol", "p", "pre", "span", "strong", "table", "tbody", "td", "tfoot", "th", "thead", "tr", "u", "ul")
	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowURLSchemes("https", "http", "mailto")
	return p
}

// SanitizeHTML removes active content, styles, forms, images, and non-allowlisted attributes.
// In particular, omitted img elements prevent email tracking resources from loading in the app.
func SanitizeHTML(raw string) string { return policy.Sanitize(raw) }
