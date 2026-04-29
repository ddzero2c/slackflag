package slackflag

import (
	"encoding/json"
	"flag"
	"fmt"
)

// metadata is what we store in Slack message metadata.event_payload.
type metadata struct {
	Args      map[string]string `json:"args"`
	Set       []string          `json:"set"`
	Invoker   string            `json:"invoker"`
	InvokedAt string            `json:"invoked_at"`
}

func encodeMetadata(fs *flag.FlagSet, invoker, invokedAt string) metadata {
	m := metadata{
		Args:      map[string]string{},
		Invoker:   invoker,
		InvokedAt: invokedAt,
	}
	fs.VisitAll(func(f *flag.Flag) {
		m.Args[f.Name] = f.Value.String()
	})
	fs.Visit(func(f *flag.Flag) {
		m.Set = append(m.Set, f.Name)
	})
	return m
}

func replayMetadata(fs *flag.FlagSet, m metadata) error {
	for _, name := range m.Set {
		v, ok := m.Args[name]
		if !ok {
			return fmt.Errorf("metadata: flag %q in Set but missing in Args", name)
		}
		if fs.Lookup(name) == nil {
			return fmt.Errorf("metadata: flag %q no longer defined (configuration drift)", name)
		}
		if err := fs.Set(name, v); err != nil {
			return fmt.Errorf("metadata: replay %q: %w", name, err)
		}
	}
	return nil
}

func (m metadata) marshal() (json.RawMessage, error) {
	return json.Marshal(m)
}

func unmarshalMetadata(data json.RawMessage) (metadata, error) {
	var m metadata
	if len(data) == 0 {
		return m, nil
	}
	err := json.Unmarshal(data, &m)
	return m, err
}
