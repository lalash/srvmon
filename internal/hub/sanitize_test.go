package hub

import "testing"

func TestSanitizeNoteRemovesScripting(t *testing.T) {
	// Each of these has defeated a regex-based sanitizer somewhere. The output
	// must not contain the marker, in any form a browser would execute.
	cases := []struct {
		name  string
		input string
		gone  string
	}{
		{"script element", `<p>ok</p><script>alert(1)</script>`, "alert"},
		{"script with attributes", `<script type="text/javascript">alert(1)</script>`, "alert"},
		{"uppercase script", `<SCRIPT>alert(1)</SCRIPT>`, "alert"},
		{"nested in allowed tag", `<p><script>alert(1)</script></p>`, "alert"},
		{"onerror handler", `<img src=x onerror="alert(1)">`, "onerror"},
		{"onclick on allowed tag", `<p onclick="alert(1)">text</p>`, "onclick"},
		{"onload uppercase", `<p ONLOAD="alert(1)">text</p>`, "ONLOAD"},
		{"javascript href", `<a href="javascript:alert(1)">click</a>`, "javascript:"},
		{"javascript href spaced", `<a href="  javascript:alert(1)">click</a>`, "javascript:"},
		{"javascript href cased", `<a href="JaVaScRiPt:alert(1)">click</a>`, "avaScRiPt:"},
		{"data uri href", `<a href="data:text/html,<script>alert(1)</script>">x</a>`, "data:"},
		{"vbscript href", `<a href="vbscript:msgbox(1)">x</a>`, "vbscript:"},
		{"style attribute", `<p style="position:fixed;top:0">x</p>`, "style"},
		{"style element", `<style>body{display:none}</style>`, "display:none"},
		{"iframe", `<iframe src="https://evil.example"></iframe>`, "iframe"},
		{"svg onload", `<svg onload="alert(1)"></svg>`, "onload"},
		{"form and input", `<form action="/x"><input name="a"></form>`, "<form"},
		{"object", `<object data="x.swf"></object>`, "object"},
		{"meta refresh", `<meta http-equiv="refresh" content="0;url=x">`, "refresh"},
		{"base tag", `<base href="https://evil.example/">`, "<base"},
		{"class and id", `<p class="a" id="b">x</p>`, "class"},
		{"srcdoc", `<iframe srcdoc="<script>alert(1)</script>"></iframe>`, "srcdoc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeNote(tc.input)
			if containsFold(got, tc.gone) {
				t.Errorf("sanitized output still contains %q\ninput:  %s\noutput: %s", tc.gone, tc.input, got)
			}
		})
	}
}

func TestSanitizeNoteKeepsFormatting(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"paragraph", `<p>hello</p>`, `<p>hello</p>`},
		{"bold and italic", `<p><strong>a</strong><em>b</em></p>`, `<p><strong>a</strong><em>b</em></p>`},
		{"heading", `<h2>Title</h2>`, `<h2>Title</h2>`},
		{"list", `<ul><li>one</li><li>two</li></ul>`, `<ul><li>one</li><li>two</li></ul>`},
		{"code", `<p><code>systemctl restart x</code></p>`, `<p><code>systemctl restart x</code></p>`},
		{"pre block", `<pre>line1
line2</pre>`, `<pre>line1
line2</pre>`},
		{"break", `<p>a<br>b</p>`, `<p>a<br>b</p>`},
		{"rule", `<hr>`, `<hr>`},
		{"rtl direction", `<p dir="rtl">سلام</p>`, `<p dir="rtl">سلام</p>`},
		{"ltr direction", `<p dir="ltr">hello</p>`, `<p dir="ltr">hello</p>`},
		{"auto direction", `<p dir="auto">mixed متن</p>`, `<p dir="auto">mixed متن</p>`},
		{"bogus direction dropped", `<p dir="sideways">x</p>`, `<p>x</p>`},
		{"unknown tag unwrapped", `<marquee>keep this text</marquee>`, `keep this text`},
		{"font tag unwrapped", `<font color="red">text</font>`, `text`},
		{"text is escaped", `<p>a < b && c</p>`, `<p>a &lt; b &amp;&amp; c</p>`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeNote(tc.input); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeNoteLinks(t *testing.T) {
	t.Run("https link is kept and made safe to open", func(t *testing.T) {
		got := SanitizeNote(`<a href="https://console.hetzner.cloud">panel</a>`)
		want := `<a href="https://console.hetzner.cloud" target="_blank" rel="noopener noreferrer">panel</a>`
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	})

	t.Run("link without a usable href loses the attribute", func(t *testing.T) {
		got := SanitizeNote(`<a href="javascript:alert(1)">x</a>`)
		if got != `<a>x</a>` {
			t.Errorf("got %q, want %q", got, `<a>x</a>`)
		}
	})
}

func TestNoteTextForSearch(t *testing.T) {
	note := `<h2>Hetzner</h2><p>renewal <strong>2026-09-01</strong></p><ul><li>port 8443</li></ul>`
	got := NoteText(note)
	want := "Hetzner renewal 2026-09-01 port 8443"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestNoteTextSkipsScriptSource(t *testing.T) {
	// A note saved before sanitizing, or restored from an old backup, must not
	// put script source into the search index.
	if got := NoteText(`<p>real</p><script>secret_token_value</script>`); containsFold(got, "secret_token") {
		t.Errorf("script contents leaked into the search text: %q", got)
	}
}

func containsFold(haystack, needle string) bool {
	return len(needle) > 0 && indexFold(haystack, needle) >= 0
}

func indexFold(haystack, needle string) int {
	hl, nl := len(haystack), len(needle)
	for i := 0; i+nl <= hl; i++ {
		if equalFold(haystack[i:i+nl], needle) {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
