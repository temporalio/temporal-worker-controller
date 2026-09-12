// Command helm-values-schema generates helm/temporal-worker-controller/values.schema.json
// from values.yaml, then deep-merges overlay.json for constraints that cannot be
// inferred (enums, patterns, extraEnv shape, and similar).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	valuesRel  = "helm/temporal-worker-controller/values.yaml"
	schemaRel  = "helm/temporal-worker-controller/values.schema.json"
	overlayRel = "hack/helm-values-schema/overlay.json"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "helm-values-schema: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(filepath.Join(root, valuesRel))
	if err != nil {
		return err
	}
	overlayJSON, err := os.ReadFile(filepath.Join(root, overlayRel))
	if err != nil {
		return err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", valuesRel, err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("%s is empty", valuesRel)
	}

	generated := schemaFromNode(doc.Content[0])
	if generated == nil {
		return fmt.Errorf("failed to infer schema from %s", valuesRel)
	}

	var overlay any
	if err := json.Unmarshal(overlayJSON, &overlay); err != nil {
		return fmt.Errorf("parse overlay.json: %w", err)
	}

	merged := deepMerge(generated, overlay)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	dest := filepath.Join(root, schemaRel)
	if err := os.WriteFile(dest, out, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", schemaRel)
	return nil
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find repo root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func schemaFromNode(n *yaml.Node) map[string]any {
	if n == nil {
		return map[string]any{"type": "object"}
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return map[string]any{"type": "object"}
		}
		return schemaFromNode(n.Content[0])
	case yaml.MappingNode:
		s := map[string]any{"type": "object"}
		if len(n.Content) == 0 {
			return s
		}
		props := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			val := n.Content[i+1]
			prop := schemaFromNode(val)
			if desc := commentDescription(key, val); desc != "" {
				prop["description"] = desc
			}
			props[key.Value] = prop
		}
		s["properties"] = props
		return s
	case yaml.SequenceNode:
		s := map[string]any{"type": "array"}
		if len(n.Content) > 0 {
			s["items"] = schemaFromNode(n.Content[0])
		}
		return s
	case yaml.ScalarNode:
		return map[string]any{"type": scalarType(n)}
	default:
		return map[string]any{"type": "object"}
	}
}

func scalarType(n *yaml.Node) any {
	switch n.Tag {
	case "!!bool":
		return "boolean"
	case "!!int":
		return "integer"
	case "!!float":
		return "number"
	case "!!null":
		return []any{"string", "null"}
	default:
		if n.Value == "true" || n.Value == "false" {
			return "boolean"
		}
		if _, err := strconv.ParseInt(n.Value, 10, 64); err == nil && n.Style != yaml.SingleQuotedStyle && n.Style != yaml.DoubleQuotedStyle {
			return "integer"
		}
		return "string"
	}
}

func commentDescription(key, val *yaml.Node) string {
	parts := []string{
		cleanComment(key.HeadComment),
		cleanComment(key.LineComment),
		cleanComment(val.HeadComment),
		cleanComment(val.LineComment),
	}
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p)
	}
	return b.String()
}

func cleanComment(c string) string {
	if c == "" {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(c, "\n") {
		line = strings.TrimRight(line, " \t")
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "#")
		if strings.HasPrefix(line, " ") {
			line = line[1:]
		}
		if line == "" {
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func deepMerge(base, overlay any) any {
	bm, bok := asMap(base)
	om, ook := asMap(overlay)
	if !bok || !ook {
		if overlay == nil {
			return base
		}
		return overlay
	}
	out := make(map[string]any, len(bm)+len(om))
	for k, v := range bm {
		out[k] = v
	}
	for k, v := range om {
		if existing, ok := out[k]; ok {
			out[k] = deepMerge(existing, v)
		} else {
			out[k] = v
		}
	}
	return out
}

func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	default:
		return nil, false
	}
}
