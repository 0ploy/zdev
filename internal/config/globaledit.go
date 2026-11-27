package config

// Editing ~/.zdev/global-config.yaml from commands (zdev claude setup): the
// file is the developer's, with their comments and order, so it is edited as
// a YAML node tree, never re-encoded from GlobalConfig. The result must still
// load and validate, or nothing is written.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// EditGlobalConfig applies edit to the global config's YAML tree and writes
// it back if the result is a valid global config.
func EditGlobalConfig(edit func(root *yaml.Node) error) error {
	path := filepath.Join(getZdevHome(), GlobalConfigFilename)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: not a YAML mapping", path)
	}
	if err := edit(doc.Content[0]); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	_ = enc.Close()

	cfg := newDefaultGlobalConfig()
	dec := yaml.NewDecoder(bytes.NewReader(buf.Bytes()))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("the edited config doesn't load (not written): %w", err)
	}
	if err := validateGlobalConfig(&cfg); err != nil {
		return fmt.Errorf("the edited config is invalid (not written): %w", err)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// YAMLMapping returns the mapping under keys in m, creating missing levels.
func YAMLMapping(m *yaml.Node, keys ...string) (*yaml.Node, error) {
	for _, k := range keys {
		v := yamlValue(m, k)
		if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == "!!null") {
			next := &yaml.Node{Kind: yaml.MappingNode}
			if v == nil {
				m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, next)
			} else {
				*v = *next
				next = v
			}
			m = next
			continue
		}
		if v.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s is not a mapping", k)
		}
		m = v
	}
	return m, nil
}

// SetYAML sets key in mapping m to the encoding of value.
func SetYAML(m *yaml.Node, key string, value any) error {
	var n yaml.Node
	if err := n.Encode(value); err != nil {
		return err
	}
	if v := yamlValue(m, key); v != nil {
		*v = n
		return nil
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &n)
	return nil
}

// DeleteYAML removes key from mapping m.
func DeleteYAML(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func yamlValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
