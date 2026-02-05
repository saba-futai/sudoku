package reverse

import "bytes"

// rewriteJavaScriptRootAbsolutePaths rewrites root-absolute URLs inside JavaScript string/template literals:
//
//	"/ws" -> "/<prefix>/ws"
//
// It intentionally does NOT touch comments/regex literals to avoid corrupting bundles.
func rewriteJavaScriptRootAbsolutePaths(in []byte, prefix string) []byte {
	p := normalizedPrefixBytes(prefix)
	if len(in) == 0 || p == nil {
		return in
	}

	const (
		jsCode = iota
		jsSingleQuote
		jsDoubleQuote
		jsTemplateRaw
		jsRegex
		jsLineComment
		jsBlockComment
	)

	var (
		out      bytes.Buffer
		last     int
		modified bool

		mode int = jsCode

		stringStart int // index of first char after opening quote

		exprDepth []int // ${ ... } brace nesting (per template expression frame)

		regexAllowed bool = true
		inCharClass  bool
		regexEscape  bool
	)
	out.Grow(len(in) + len(in)/16)

	flushInsertPrefix := func(slashIndex int) {
		out.Write(in[last:slashIndex])
		out.Write(p)
		last = slashIndex
		modified = true
	}

	for i := 0; i < len(in); i++ {
		c := in[i]

		switch mode {
		case jsCode:
			if len(exprDepth) > 0 {
				if c == '{' {
					exprDepth[len(exprDepth)-1]++
				} else if c == '}' {
					top := len(exprDepth) - 1
					if exprDepth[top] == 0 {
						exprDepth = exprDepth[:top]
						mode = jsTemplateRaw
						continue
					}
					exprDepth[top]--
				}
			}

			if isSpace(c) {
				continue
			}

			switch c {
			case '\'':
				mode = jsSingleQuote
				stringStart = i + 1
				continue
			case '"':
				mode = jsDoubleQuote
				stringStart = i + 1
				continue
			case '`':
				mode = jsTemplateRaw
				continue
			case '/':
				if i+1 < len(in) {
					switch in[i+1] {
					case '/':
						mode = jsLineComment
						i++
						continue
					case '*':
						mode = jsBlockComment
						i++
						continue
					}
				}
				if regexAllowed {
					mode = jsRegex
					inCharClass = false
					regexEscape = false
					continue
				}
				// Division operator.
				regexAllowed = true
				continue
			case '(',
				'[', '{',
				',', ';', ':',
				'=', '!', '?',
				'*', '%', '&', '|', '^', '~',
				'<', '>':
				regexAllowed = true
				continue
			case ')', ']':
				regexAllowed = false
				continue
			case '}':
				// If this was a template expr close, we'd have continued above.
				regexAllowed = false
				continue
			case '+', '-':
				if i+1 < len(in) && in[i+1] == c {
					// ++ / -- can end an expression.
					regexAllowed = false
					i++
					continue
				}
				regexAllowed = true
				continue
			default:
			}

			if isIdentStart(c) {
				j := i + 1
				for j < len(in) && isIdentContinue(in[j]) {
					j++
				}
				if keywordExpectsExpr(in[i:j]) {
					regexAllowed = true
				} else {
					regexAllowed = false
				}
				i = j - 1
				continue
			}

			if isDigit(c) {
				// Numeric literals can end an expression; no need to fully lex the number here.
				regexAllowed = false
				continue
			}

			// Any other token: keep regexAllowed as-is.
		case jsSingleQuote, jsDoubleQuote:
			if c == '\\' {
				if i+1 < len(in) {
					i++
				}
				continue
			}
			delim := byte('\'')
			if mode == jsDoubleQuote {
				delim = '"'
			}
			if c == delim {
				mode = jsCode
				regexAllowed = false
				continue
			}
			if i == stringStart && c == '/' {
				if i+1 < len(in) && in[i+1] == delim {
					// Bare "/" (often used as separators) is not necessarily a URL.
					continue
				}
				if i+1 < len(in) && in[i+1] == '/' {
					// Protocol-relative: keep.
					continue
				}
				if urlHasPathPrefix(in[i:], p) {
					continue
				}
				flushInsertPrefix(i)
				continue
			}
		case jsTemplateRaw:
			if c == '\\' {
				if i+1 < len(in) {
					i++
				}
				continue
			}
			if c == '`' {
				mode = jsCode
				regexAllowed = false
				continue
			}
			if c == '$' && i+1 < len(in) && in[i+1] == '{' {
				mode = jsCode
				regexAllowed = true
				exprDepth = append(exprDepth, 0)
				i++
				continue
			}

			if i > 0 && in[i-1] == '`' && c == '/' {
				if i+1 < len(in) && in[i+1] == '`' {
					// Bare "/" in a template literal.
					continue
				}
				if i+1 < len(in) && in[i+1] == '/' {
					continue
				}
				if urlHasPathPrefix(in[i:], p) {
					continue
				}
				flushInsertPrefix(i)
				continue
			}
		case jsRegex:
			if regexEscape {
				regexEscape = false
				continue
			}
			if c == '\\' {
				regexEscape = true
				continue
			}
			if c == '[' && !inCharClass {
				inCharClass = true
				continue
			}
			if c == ']' && inCharClass {
				inCharClass = false
				continue
			}
			if c == '/' && !inCharClass {
				mode = jsCode
				regexAllowed = false
				continue
			}
		case jsLineComment:
			if c == '\n' {
				mode = jsCode
			}
		case jsBlockComment:
			if c == '*' && i+1 < len(in) && in[i+1] == '/' {
				mode = jsCode
				i++
			}
		}
	}

	if !modified {
		return in
	}
	out.Write(in[last:])
	return out.Bytes()
}

func isIdentStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_' || b == '$'
}

func isIdentContinue(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func keywordExpectsExpr(word []byte) bool {
	// Keywords after which a regex literal may appear as the next token.
	switch len(word) {
	case 2:
		return (word[0] == 'i' && word[1] == 'n') || (word[0] == 'o' && word[1] == 'f')
	case 3:
		return word[0] == 'n' && word[1] == 'e' && word[2] == 'w'
	case 4:
		return (word[0] == 'c' && word[1] == 'a' && word[2] == 's' && word[3] == 'e') ||
			(word[0] == 'v' && word[1] == 'o' && word[2] == 'i' && word[3] == 'd')
	case 5:
		return (word[0] == 't' && word[1] == 'h' && word[2] == 'r' && word[3] == 'o' && word[4] == 'w') ||
			(word[0] == 'y' && word[1] == 'i' && word[2] == 'e' && word[3] == 'l' && word[4] == 'd') ||
			(word[0] == 'a' && word[1] == 'w' && word[2] == 'a' && word[3] == 'i' && word[4] == 't')
	case 6:
		return (word[0] == 'r' && word[1] == 'e' && word[2] == 't' && word[3] == 'u' && word[4] == 'r' && word[5] == 'n') ||
			(word[0] == 'd' && word[1] == 'e' && word[2] == 'l' && word[3] == 'e' && word[4] == 't' && word[5] == 'e') ||
			(word[0] == 't' && word[1] == 'y' && word[2] == 'p' && word[3] == 'e' && word[4] == 'o' && word[5] == 'f')
	case 10:
		return word[0] == 'i' && word[1] == 'n' && word[2] == 's' && word[3] == 't' && word[4] == 'a' &&
			word[5] == 'n' && word[6] == 'c' && word[7] == 'e' && word[8] == 'o' && word[9] == 'f'
	default:
		return false
	}
}
