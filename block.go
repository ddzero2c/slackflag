package slackflag

import "github.com/slack-go/slack"

// Block is a Block Kit block — an alias for slack.Block. Build one with
// Section, Header, Fields, Divider, or Actions for convenience, or pass any
// slack-go block directly.
type Block = slack.Block

// Element is an interactive Block Kit element (e.g. Button) for use inside
// Actions — an alias for slack.BlockElement.
type Element = slack.BlockElement

// Section returns a mrkdwn section block.
func Section(text string) Block {
	return slack.NewSectionBlock(
		slack.NewTextBlockObject(slack.MarkdownType, text, false, false), nil, nil)
}

// Header returns a plain_text header block.
func Header(text string) Block {
	return slack.NewHeaderBlock(
		slack.NewTextBlockObject(slack.PlainTextType, text, true, false))
}

// Fields returns a section with key/value mrkdwn fields. kv is flat: k1, v1, k2, v2, ...
// Panics if len(kv) is odd.
func Fields(kv ...string) Block {
	if len(kv)%2 != 0 {
		panic("slackflag.Fields: odd number of arguments")
	}
	objs := make([]*slack.TextBlockObject, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		objs = append(objs, slack.NewTextBlockObject(slack.MarkdownType, "*"+kv[i]+"*\n"+kv[i+1], false, false))
	}
	return slack.NewSectionBlock(nil, objs, nil)
}

// Divider returns a divider block.
func Divider() Block { return slack.NewDividerBlock() }

// Button returns an interactive button element. style is "" (default),
// "primary", or "danger". value travels back to the handler as
// Interaction.Value when the button is clicked.
func Button(actionID, text, value, style string) Element {
	btn := slack.NewButtonBlockElement(actionID, value,
		slack.NewTextBlockObject(slack.PlainTextType, text, false, false))
	switch style {
	case "primary":
		btn.Style = slack.StylePrimary
	case "danger":
		btn.Style = slack.StyleDanger
	}
	return btn
}

// Actions returns an actions block holding interactive elements.
func Actions(elements ...Element) Block {
	return slack.NewActionBlock("", elements...)
}
