package config

// Editing ~/.zdev/global-config.yaml and .zdev/config.yaml from commands
// (zdev agent setup): the files are the developer's, with their comments and
// order, so they are edited as YAML node trees, never re-encoded from the
// config structs. The result must still load, or nothing is written.

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
	return editYAMLFile(filepath.Join(getZdevHome(), GlobalConfigFilename), edit, func(data []byte) error {
		cfg := newDefaultGlobalConfig()
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return err
		}
		return validateGlobalConfig(&cfg)
	})
}

// EditProjectConfig applies edit to dir's .zdev/config.yaml the same way
// (comments and order kept). The result must still parse with known fields
// only; variables are not substituted here, so ${VAR} values pass as text.
func EditProjectConfig(dir string, edit func(root *yaml.Node) error) error {
	return editYAMLFile(filepath.Join(dir, ".zdev", "config.yaml"), edit, func(data []byte) error {
		var cfg ProjectConfig
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		return dec.Decode(&cfg)
	})
}

// editYAMLFile edits path as a YAML node tree and writes it back, in place of
// the old file, only when check accepts the result.
func editYAMLFile(path string, edit func(root *yaml.Node) error, check func([]byte) error) error {
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
	if err := check(buf.Bytes()); err != nil {
		return fmt.Errorf("%s: the edited file doesn't load (not written): %w", path, err)
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
