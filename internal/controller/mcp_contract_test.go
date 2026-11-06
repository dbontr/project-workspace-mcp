package controller

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPToolCatalogContract(t *testing.T) {
	if len(tools) == 0 {
		t.Fatal("MCP tool catalog is empty")
	}
	seen := make(map[string]bool, len(tools))
	for _, definition := range tools {
		if definition.Name == "" || strings.ContainsAny(definition.Name, " \t\r\n/") {
			t.Fatalf("invalid MCP tool name %q", definition.Name)
		}
		if seen[definition.Name] {
			t.Fatalf("duplicate MCP tool %q", definition.Name)
		}
		seen[definition.Name] = true
		if definition.Description == "" {
			t.Fatalf("missing description for tool %s", definition.Name)
		}
		schema := definition.schema()
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s must expose a bounded object argument schema", definition.Name)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s has invalid properties", definition.Name)
		}
		required, ok := schema["required"].([]string)
		if !ok {
			t.Fatalf("%s has an invalid required array", definition.Name)
		}
		for _, key := range required {
			if _, exists := properties[key]; !exists {
				t.Fatalf("%s requires an undeclared argument %s", definition.Name, key)
			}
		}
		if _, err := json.Marshal(schema); err != nil {
			t.Fatalf("%s has invalid JSON schema: %v", definition.Name, err)
		}
	}
}
