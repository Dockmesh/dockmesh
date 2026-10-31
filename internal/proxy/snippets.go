package proxy

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// cfToken is one Caddyfile token with its byte span in the source.
type cfToken struct {
	text       string
	start, end int
	line       int // line the token starts on
	endLine    int // line the token ends on
	quoted     bool
}

func (t cfToken) is(text string) bool { return !t.quoted && t.text == text }

var heredocMarker = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func continuation(s string) bool {
	return strings.HasPrefix(s, "\\\n") || strings.HasPrefix(s, "\\\r\n")
}

// lexCaddyfile splits text into tokens following Caddy's lexer for
// quotes, backticks, comments, line continuations and heredocs.
func lexCaddyfile(s string) []cfToken {
	var toks []cfToken
	line := 1
	i := 0
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\n':
			line++
			i++
			continue
		case continuation(s[i:]):
			i = strings.IndexByte(s[i:], '\n') + i + 1
			continue
		case unicode.IsSpace(r):
			i += n
			continue
		case r == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		t := cfToken{start: i, line: line}
		var val strings.Builder
		if r == '"' || r == '`' {
			quote := s[i]
			t.quoted = true
			i++
			for i < len(s) && s[i] != quote {
				if quote == '"' && s[i] == '\\' && i+1 < len(s) {
					if s[i+1] != '"' {
						val.WriteByte('\\')
					}
					i++
				}
				if s[i] == '\n' {
					line++
				}
				val.WriteByte(s[i])
				i++
			}
			i = min(i+1, len(s))
		} else {
			for i < len(s) {
				r, n := utf8.DecodeRuneInString(s[i:])
				if unicode.IsSpace(r) || continuation(s[i:]) {
					break
				}
				if r == '\\' && val.Len() == 0 && strings.HasPrefix(s[i+1:], "<") {
					val.WriteByte('<')
					i += 2
					continue
				}
				val.WriteString(s[i : i+n])
				i += n
			}
			text := val.String()
			rest := strings.TrimPrefix(s[i:], "\r")
			if marker, ok := strings.CutPrefix(text, "<<"); ok && s[t.start] != '\\' &&
				strings.HasPrefix(rest, "\n") && heredocMarker.MatchString(marker) {
				body := rest[1:]
				content := body
				i = len(s)
				if end := strings.Index(body, marker); end >= 0 {
					content = body[:end]
					i = len(s) - len(body) + end + len(marker)
				}
				line += 1 + strings.Count(content, "\n")
				t.quoted = true
				val.Reset()
				val.WriteString(content)
			}
		}
		t.text = val.String()
		t.end = i
		t.endLine = line
		toks = append(toks, t)
	}
	return toks
}

const (
	maxSnippetDepth   = 16
	maxSnippetImports = 1000
)

// expandSnippets replaces every `import <name>` of a snippet defined
// earlier in the same text by the snippet's content and drops the
// snippet definitions, so Caddy never resolves an import itself: an
// import Caddy can't match to a snippet is read as a file path. Imports
// with arguments or a block are refused, because Caddy substitutes the
// arguments into the snippet text, where they could form a path again.
// What isn't expanded is left for screenCaddyfile to refuse.
func expandSnippets(text string) (string, error) {
	e := &snippetExpander{snippets: map[string]string{}}
	return e.expand(strings.TrimPrefix(text, "\uFEFF"), nil)
}

type snippetExpander struct {
	snippets map[string]string
	imports  int
}

// expand works on the whole file when stack is empty and on a snippet's
// content otherwise; snippets can only be defined at the top of the file.
func (e *snippetExpander) expand(text string, stack []string) (string, error) {
	toks := lexCaddyfile(text)
	var b strings.Builder
	last, depth := 0, 0
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		newLine := i == 0 || toks[i-1].endLine < t.line
		switch {
		case t.is("{"):
			depth++
		case t.is("}"):
			depth--
		}
		if len(stack) == 0 && depth == 0 && newLine && !t.quoted && len(t.text) >= 3 &&
			strings.HasPrefix(t.text, "(") && strings.HasSuffix(t.text, ")") &&
			i+1 < len(toks) && toks[i+1].is("{") && toks[i+1].line == t.endLine {
			name := t.text[1 : len(t.text)-1]
			if _, dup := e.snippets[name]; dup {
				return "", fmt.Errorf("%w: snippet (%s) is defined twice", ErrCaddyfileRejected, name)
			}
			end := closingBrace(toks, i+1)
			if end < 0 {
				return "", fmt.Errorf("%w: snippet (%s) has no closing brace", ErrCaddyfileRejected, name)
			}
			e.snippets[name] = text[toks[i+1].end:toks[end].start]
			b.WriteString(text[last:t.start])
			b.WriteString(strings.Repeat("\n", strings.Count(text[t.start:toks[end].end], "\n")))
			last = toks[end].end
			i = end
			continue
		}
		directive := newLine || toks[i-1].is("{") || toks[i-1].is("}")
		if !t.is("import") || !directive {
			continue
		}
		var args []cfToken
		for j := i + 1; j < len(toks) && toks[j].line == toks[j-1].endLine && !toks[j].is("}"); j++ {
			args = append(args, toks[j])
		}
		if len(args) == 0 {
			return "", fmt.Errorf("%w: import needs a snippet name", ErrCaddyfileRejected)
		}
		name := args[0].text
		if len(args) > 1 {
			return "", fmt.Errorf("%w: import %s: arguments and blocks are not supported", ErrCaddyfileRejected, name)
		}
		body, ok := e.snippets[name]
		if !ok {
			return "", fmt.Errorf("%w: import %s: only snippets defined earlier in the same Caddyfile can be imported; paste the file's content instead", ErrCaddyfileRejected, name)
		}
		for _, s := range stack {
			if s == name {
				return "", fmt.Errorf("%w: snippet (%s) imports itself", ErrCaddyfileRejected, name)
			}
		}
		if e.imports++; e.imports > maxSnippetImports || len(stack) >= maxSnippetDepth {
			return "", fmt.Errorf("%w: more than %d snippet imports or nested deeper than %d", ErrCaddyfileRejected, maxSnippetImports, maxSnippetDepth)
		}
		if strings.Contains(body, "{args") || strings.Contains(body, "{block") {
			return "", fmt.Errorf("%w: snippet (%s) uses {args} or {block}, which need import arguments", ErrCaddyfileRejected, name)
		}
		expanded, err := e.expand(body, append(stack, name))
		if err != nil {
			return "", err
		}
		b.WriteString(text[last:t.start])
		b.WriteString("\n" + expanded + "\n")
		last = args[0].end
		i++
		if b.Len()+len(text)-last > MaxCaddyfileSize {
			return "", fmt.Errorf("%w: larger than %d KiB with snippets expanded", ErrCaddyfileRejected, MaxCaddyfileSize>>10)
		}
	}
	b.WriteString(text[last:])
	return b.String(), nil
}

// closingBrace returns the index of the token closing the block opened
// at toks[open], or -1.
func closingBrace(toks []cfToken, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch {
		case toks[i].is("{"):
			depth++
		case toks[i].is("}"):
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
