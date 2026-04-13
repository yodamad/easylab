package tfparse

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// TFVariable represents a Terraform variable block extracted from .tf files.
type TFVariable struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
	Sensitive   bool   `json:"sensitive"`
}

// ParseVariablesFromFile reads a single .tf file and extracts variable blocks.
func ParseVariablesFromFile(path string) ([]TFVariable, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	return ParseVariablesFromBytes(content, filepath.Base(path))
}

// ParseVariablesFromZip opens a .zip archive and parses all .tf files inside
// for variable blocks. Variables are deduplicated by name (last definition wins).
func ParseVariablesFromZip(zipPath string) ([]TFVariable, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	seen := make(map[string]TFVariable)
	var order []string

	for _, f := range r.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(f.Name), ".tf") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s in zip: %w", f.Name, err)
		}

		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read %s in zip: %w", f.Name, err)
		}

		vars, err := ParseVariablesFromBytes(content, f.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", f.Name, err)
		}
		for _, v := range vars {
			if _, exists := seen[v.Name]; !exists {
				order = append(order, v.Name)
			}
			seen[v.Name] = v
		}
	}

	result := make([]TFVariable, 0, len(order))
	for _, name := range order {
		result = append(result, seen[name])
	}
	return result, nil
}

// ParseVariablesFromBytes parses HCL content and extracts variable blocks.
func ParseVariablesFromBytes(content []byte, filename string) ([]TFVariable, error) {
	parser := hclparse.NewParser()
	file, diags := parser.ParseHCL(content, filename)
	if diags.HasErrors() {
		return nil, fmt.Errorf("failed to parse HCL: %s", diags.Error())
	}

	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("unexpected HCL body type")
	}

	var variables []TFVariable
	for _, block := range body.Blocks {
		if block.Type != "variable" || len(block.Labels) == 0 {
			continue
		}

		v := TFVariable{
			Name:     block.Labels[0],
			Required: true,
		}

		attrs, _ := block.Body.JustAttributes()
		if desc, ok := attrs["description"]; ok {
			v.Description = evaluateStringAttr(desc)
		}
		if typ, ok := attrs["type"]; ok {
			v.Type = strings.TrimSpace(string(typ.Expr.Range().SliceBytes(content)))
		}
		if def, ok := attrs["default"]; ok {
			v.Default = evaluateDefaultAttr(def, content)
			v.Required = false
		}
		if sens, ok := attrs["sensitive"]; ok {
			v.Sensitive = evaluateBoolAttr(sens)
		}

		variables = append(variables, v)
	}

	return variables, nil
}

func evaluateStringAttr(attr *hcl.Attribute) string {
	val, diags := attr.Expr.Value(&hcl.EvalContext{})
	if diags.HasErrors() || val.Type() != cty.String {
		return ""
	}
	return val.AsString()
}

func evaluateBoolAttr(attr *hcl.Attribute) bool {
	val, diags := attr.Expr.Value(&hcl.EvalContext{})
	if diags.HasErrors() || val.Type() != cty.Bool {
		return false
	}
	return val.True()
}

func evaluateDefaultAttr(attr *hcl.Attribute, src []byte) string {
	val, diags := attr.Expr.Value(&hcl.EvalContext{})
	if diags.HasErrors() {
		return strings.TrimSpace(string(attr.Expr.Range().SliceBytes(src)))
	}
	switch val.Type() {
	case cty.String:
		return val.AsString()
	case cty.Number:
		bf := val.AsBigFloat()
		return bf.Text('f', -1)
	case cty.Bool:
		if val.True() {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(string(attr.Expr.Range().SliceBytes(src)))
	}
}
