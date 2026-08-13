package hub

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Notes are stored as HTML, so they are sanitized before they are ever handed
// back to a browser. This walks a real parse tree rather than matching patterns:
// a regex sanitizer is defeated by any of the dozens of ways to write a tag that
// a parser accepts and the pattern does not, which is why they keep failing.

// allowedTags is what a note may contain. Everything else is unwrapped — the
// element goes, its text stays — so pasting from a word processor loses the
// markup instead of the content.
var allowedTags = map[atom.Atom]bool{
	atom.P: true, atom.Br: true, atom.Div: true, atom.Span: true,
	atom.Strong: true, atom.B: true, atom.Em: true, atom.I: true,
	atom.U: true, atom.S: true, atom.Strike: true,
	atom.H2: true, atom.H3: true, atom.H4: true,
	atom.Ul: true, atom.Ol: true, atom.Li: true,
	atom.A: true, atom.Code: true, atom.Pre: true,
	atom.Blockquote: true, atom.Hr: true,
}

// Elements whose *contents* are dropped along with them: leaving the text of a
// <script> in place would put source code in the note.
var strippedSubtrees = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true,
	atom.Embed: true, atom.Form: true, atom.Input: true, atom.Button: true,
	atom.Textarea: true, atom.Select: true, atom.Link: true, atom.Meta: true,
	atom.Svg: true, atom.Math: true, atom.Template: true, atom.Base: true,
}

var allowedDirections = map[string]bool{"ltr": true, "rtl": true, "auto": true}

// SanitizeNote returns the note with only the allowed subset of HTML left. It
// never returns an error: unparseable input yields the escaped text, which is
// safe and preserves what the operator wrote.
func SanitizeNote(input string) string {
	nodes, err := html.ParseFragment(strings.NewReader(input), &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	if err != nil {
		return html.EscapeString(input)
	}

	var out strings.Builder
	for _, node := range nodes {
		writeClean(&out, node)
	}
	return strings.TrimSpace(out.String())
}

func writeClean(out *strings.Builder, node *html.Node) {
	switch node.Type {
	case html.TextNode:
		out.WriteString(html.EscapeString(node.Data))
		return
	case html.ElementNode:
		// fall through
	default:
		// Comments, doctypes and anything else carry nothing worth keeping.
		return
	}

	if strippedSubtrees[node.DataAtom] {
		return
	}

	if !allowedTags[node.DataAtom] {
		// Unwrap: keep the children, drop the element itself.
		writeChildren(out, node)
		return
	}

	tag := node.Data
	out.WriteString("<" + tag)
	for _, attr := range cleanAttrs(node) {
		out.WriteString(" " + attr.Key + `="` + html.EscapeString(attr.Val) + `"`)
	}

	if node.DataAtom == atom.Br || node.DataAtom == atom.Hr {
		out.WriteString(">")
		return
	}
	out.WriteString(">")
	writeChildren(out, node)
	out.WriteString("</" + tag + ">")
}

func writeChildren(out *strings.Builder, node *html.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		writeClean(out, child)
	}
}

// cleanAttrs keeps dir on anything and href on links, and nothing else. That
// drops every on* handler and style attribute without having to enumerate them.
func cleanAttrs(node *html.Node) []html.Attribute {
	var kept []html.Attribute
	for _, attr := range node.Attr {
		switch strings.ToLower(attr.Key) {
		case "dir":
			if allowedDirections[strings.ToLower(attr.Val)] {
				kept = append(kept, html.Attribute{Key: "dir", Val: strings.ToLower(attr.Val)})
			}
		case "href":
			if node.DataAtom == atom.A && safeURL(attr.Val) {
				kept = append(kept, html.Attribute{Key: "href", Val: attr.Val})
			}
		}
	}
	if node.DataAtom == atom.A {
		hasHref := false
		for _, attr := range kept {
			if attr.Key == "href" {
				hasHref = true
			}
		}
		if hasHref {
			kept = append(kept,
				html.Attribute{Key: "target", Val: "_blank"},
				html.Attribute{Key: "rel", Val: "noopener noreferrer"})
		}
	}
	return kept
}

// safeURL rejects javascript:, data: and every other scheme that turns a link
// into script execution. Relative links are fine — they cannot leave the panel.
func safeURL(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	// Control characters are used to smuggle "java\tscript:" past naive checks.
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	lower := strings.ToLower(trimmed)
	scheme, _, hasScheme := strings.Cut(lower, ":")
	if !hasScheme || strings.ContainsAny(scheme, "/?#") {
		return true // relative
	}
	return scheme == "http" || scheme == "https" || scheme == "mailto"
}

// NoteText flattens a note to plain text for searching, so a query matches what
// the operator sees rather than the tag names around it.
func NoteText(note string) string {
	nodes, err := html.ParseFragment(strings.NewReader(note), &html.Node{
		Type: html.ElementNode, Data: "body", DataAtom: atom.Body,
	})
	if err != nil {
		return note
	}

	var out strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
			return
		}
		if node.Type == html.ElementNode && strippedSubtrees[node.DataAtom] {
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if node.Type == html.ElementNode {
			switch node.DataAtom {
			case atom.P, atom.Br, atom.Div, atom.Li, atom.H2, atom.H3, atom.H4, atom.Pre:
				out.WriteString(" ")
			}
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	return strings.Join(strings.Fields(out.String()), " ")
}
