package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

func main() {
	source := flag.String("source", "", "Terraform module source (required)")
	version := flag.String("version", "", "Module version (optional)")
	name := flag.String("name", "", "Wrapper module name (optional)")
	iterable := flag.Bool("iterable", false, "Set to true to create a module that iterates over a map of resources")
	flag.Parse()

	if *source == "" {
		log.Fatal("Error: -source is required")
	}

	// Determine module name
	modName := *name
	if modName == "" {
		parts := strings.Split(strings.Trim(*source, "/"), "/")
		modName = parts[len(parts)-1]
		modName = strings.TrimSuffix(modName, ".git")
	}

	// Create a temporary directory to download the module
	tmpDir, err := os.MkdirTemp("", "tfwrapper-")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Download the module using 'tofu get'
	modulePath, err := downloadModule(*source, *version, tmpDir)
	if err != nil {
		log.Fatalf("Failed to download module: %v", err)
	}

	// Parse variables.tf
	vars, err := parseVariables(filepath.Join(modulePath, "variables.tf"))
	if err != nil {
		log.Fatalf("Failed to parse variables.tf: %v", err)
	}

	// Create wrapper directory
	if err := os.Mkdir(modName, 0755); err != nil && !os.IsExist(err) {
		log.Fatalf("Failed to create directory: %v", err)
	}

	// Write locals.tf
	locals := `locals {
  config = jsondecode(var.config)
}
`
	writeFile(modName, "locals.tf", locals)

	// Write variables.tf
	variables := fmt.Sprintf(`variable "config" {
  type        = any
  description = "A JSON encoded object that contains the full %s config"
  default     = "{}"
}
`, modName)
	writeFile(modName, "variables.tf", variables)

	// Write main.tf
	mainTf := generateMainTf(*source, *version, *iterable, vars)
	writeFile(modName, "main.tf", mainTf)

	// Write outputs.tf
	outputs := `output "output" {
  value = module.this
}
`
	writeFile(modName, "outputs.tf", outputs)

	fmt.Printf("Wrapper module created in ./%s\n", modName)
}

func writeFile(dir, name, content string) {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		log.Fatalf("Failed to write %s: %v", name, err)
	}

	// Attempt to format the file if it's a .tf file
	if strings.HasSuffix(name, ".tf") {
		src, err := os.ReadFile(path)
		if err == nil {
			doc, diag := hclwrite.ParseConfig(src, path, hcl.InitialPos)
			if diag == nil || !diag.HasErrors() {
				// Overwrite with formatted content
				_ = os.WriteFile(path, doc.Bytes(), 0644)
			}
		}
	}
}

func downloadModule(source, version, destDir string) (string, error) {
	// Parse the module source to handle submodule paths
	parts := strings.SplitN(source, "//", 2)
	moduleSource := parts[0]
	subPath := ""
	if len(parts) > 1 {
		subPath = parts[1]
	}

	// Convert registry modules to GitHub URLs
	if !strings.Contains(moduleSource, "://") && !strings.HasPrefix(moduleSource, "github.com/") {
		// This looks like a registry module (e.g., "terraform-aws-modules/iam/aws")
		// Convert to GitHub URL - remove the "/aws" provider suffix for GitHub
		sourceParts := strings.Split(moduleSource, "/")
		if len(sourceParts) >= 3 {
			// Format: terraform-aws-modules/iam/aws -> terraform-aws-modules/terraform-aws-iam
			org := sourceParts[0]
			name := sourceParts[1]
			provider := sourceParts[2]
			moduleSource = fmt.Sprintf("https://github.com/%s/terraform-%s-%s.git", org, provider, name)
		} else {
			moduleSource = "https://github.com/" + moduleSource + ".git"
		}
	} else if strings.HasPrefix(moduleSource, "github.com/") {
		// Add https:// prefix
		moduleSource = "https://" + moduleSource + ".git"
	}

	// Clone the repository
	repoDir := filepath.Join(destDir, "repo")
	cmd := exec.Command("git", "clone", "--depth=1", moduleSource, repoDir)
	if version != "" {
		// For tagged versions, we need to fetch the specific tag
		cmd = exec.Command("git", "clone", "--depth=1", "--branch", version, moduleSource, repoDir)
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to clone repository %s: %w", moduleSource, err)
	}

	// Determine the final module path
	modulePath := repoDir
	if subPath != "" {
		modulePath = filepath.Join(repoDir, subPath)
	}

	// Verify the module directory exists and contains variables.tf
	variablesPath := filepath.Join(modulePath, "variables.tf")
	if _, err := os.Stat(variablesPath); os.IsNotExist(err) {
		return "", fmt.Errorf("variables.tf not found in module path %s", modulePath)
	}

	return modulePath, nil
}

func parseVariables(filePath string) (map[string]string, error) {
	src, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read variables file: %w", err)
	}

	parser := hclparse.NewParser()
	file, diags := parser.ParseHCL(src, filepath.Base(filePath))
	if diags.HasErrors() {
		return nil, fmt.Errorf("failed to parse HCL: %s", diags.Error())
	}

	content, _, diags := file.Body.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "variable", LabelNames: []string{"name"}},
		},
	})
	if diags.HasErrors() {
		return nil, fmt.Errorf("failed to decode HCL: %s", diags.Error())
	}

	vars := make(map[string]string)
	for _, block := range content.Blocks {
		if block.Type == "variable" {
			varName := block.Labels[0]
			attrs, _ := block.Body.JustAttributes()
			var defaultValue string
			if defAttr, ok := attrs["default"]; ok {
				val, diags := defAttr.Expr.Value(nil)
				if diags.HasErrors() {
					// Could not statically evaluate, use the expression as a string
					start := defAttr.Expr.Range().Start.Byte
					end := defAttr.Expr.Range().End.Byte
					defaultValue = string(src[start:end])
				} else {
					defaultValue = ctyValueToString(val)
				}
			} else {
				defaultValue = "null" // No default value
			}
			vars[varName] = defaultValue
		}
	}
	return vars, nil
}

func ctyValueToString(val cty.Value) string {
	if val.IsNull() {
		return "null"
	}
	if val.Type().IsPrimitiveType() {
		switch val.Type().FriendlyName() {
		case "string":
			return fmt.Sprintf("\"%s\"", val.AsString())
		case "number":
			return fmt.Sprintf("%v", val.AsBigFloat())
		case "bool":
			return fmt.Sprintf("%v", val.True())
		default:
			return fmt.Sprintf("%v", val.GoString())
		}
	}
	// For complex types, return a string representation
	// This part might need to be more sophisticated for production use
	// For now, we will use a simplified JSON-like representation
	if val.Type().IsObjectType() || val.Type().IsMapType() {
		return "{}"
	}
	if val.Type().IsTupleType() || val.Type().IsListType() {
		return "[]"
	}
	return "null"
}

func generateMainTf(source, version string, iterable bool, vars map[string]string) string {
	var builder strings.Builder

	// Add header comment with version info
	if version != "" {
		builder.WriteString(fmt.Sprintf("# Module source: %s\n# Version: %s\n\n", source, version))
	} else {
		builder.WriteString(fmt.Sprintf("# Module source: %s\n# Version: latest (no version constraint specified)\n\n", source))
	}

	builder.WriteString("module \"this\" {\n")
	builder.WriteString(fmt.Sprintf("  source = \"%s\"\n", source))
	if version != "" {
		builder.WriteString(fmt.Sprintf("  version = \"%s\"\n", version))
	}

	// Add empty line before variables
	builder.WriteString("\n")

	var configPrefix string
	if iterable {
		builder.WriteString("  for_each = try(local.config, {})\n\n")
		configPrefix = "each.value."
	} else {
		configPrefix = "local.config."
	}

	// Sort variable names alphabetically for predictable output
	varNames := make([]string, 0, len(vars))
	for name := range vars {
		varNames = append(varNames, name)
	}
	sort.Strings(varNames)

	// Add sorted variables
	for _, name := range varNames {
		def := vars[name]
		builder.WriteString(fmt.Sprintf("  %s = try(%s%s, %s)\n", name, configPrefix, name, def))
	}

	builder.WriteString("}\n")
	return builder.String()
}
