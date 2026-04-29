package slackflag

import "fmt"

// tokenize splits a Slack slash command's text field into argv-style tokens.
// Supports double-quoted, single-quoted, and backslash-escaped sequences.
func tokenize(s string) ([]string, error) {
	var (
		tokens  []string
		cur     []byte
		inToken bool
	)
	push := func() {
		if inToken {
			tokens = append(tokens, string(cur))
			cur = cur[:0]
			inToken = false
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			push()
			i++
		case c == '"':
			inToken = true
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					cur = append(cur, s[i+1])
					i += 2
					continue
				}
				cur = append(cur, s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unmatched double quote")
			}
			i++
		case c == '\'':
			inToken = true
			i++
			for i < len(s) && s[i] != '\'' {
				cur = append(cur, s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unmatched single quote")
			}
			i++
		case c == '\\':
			if i+1 >= len(s) {
				return nil, fmt.Errorf("trailing backslash")
			}
			cur = append(cur, s[i+1])
			inToken = true
			i += 2
		default:
			cur = append(cur, c)
			inToken = true
			i++
		}
	}
	push()
	return tokens, nil
}
