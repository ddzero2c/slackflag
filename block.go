package slackflag

// Block is anything that JSON-marshals to a valid Slack Block Kit block.
// Type alias keeps the package zero-dep but compatible with slack-go/slack types.
type Block = any

// Section returns a mrkdwn section block.
func Section(text string) Block {
	return map[string]any{
		"type": "section",
		"text": map[string]any{"type": "mrkdwn", "text": text},
	}
}

// Header returns a plain_text header block.
func Header(text string) Block {
	return map[string]any{
		"type": "header",
		"text": map[string]any{"type": "plain_text", "text": text, "emoji": true},
	}
}

// Fields returns a section with key/value mrkdwn fields. kv is flat: k1, v1, k2, v2, ...
// Panics if len(kv) is odd.
func Fields(kv ...string) Block {
	if len(kv)%2 != 0 {
		panic("slackflag.Fields: odd number of arguments")
	}
	fields := make([]map[string]any, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		fields = append(fields, map[string]any{
			"type": "mrkdwn",
			"text": "*" + kv[i] + "*\n" + kv[i+1],
		})
	}
	return map[string]any{
		"type":   "section",
		"fields": fields,
	}
}

// Divider returns a divider block.
func Divider() Block {
	return map[string]any{"type": "divider"}
}
