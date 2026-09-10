package template

import (
	"encoding/json"
	"reflect"
	"strings"
)

// ContractTypes is the public, data-only SDK type surface. Execution remains
// owned by the host; acquisition options are declared in the SDK generator.
func ContractTypes() []reflect.Type {
	return []reflect.Type{reflect.TypeFor[Document](), reflect.TypeFor[Spec](), reflect.TypeFor[Localization](), reflect.TypeFor[IconAsset](), reflect.TypeFor[GitSource](), reflect.TypeFor[ResolvedSource](), reflect.TypeFor[Snapshot](), reflect.TypeFor[SourceCatalog]()}
}

func SchemaFor(t reflect.Type) map[string]any {
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}
	}
	if t.Kind() == reflect.Pointer {
		return SchemaFor(t.Elem())
	}
	if t == reflect.TypeFor[[]byte]() {
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	}
	switch t.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "" || tag[0] == "-" {
				continue
			}
			properties[tag[0]] = SchemaFor(f.Type)
			if len(tag) == 1 {
				required = append(required, tag[0])
			}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": SchemaFor(t.Elem())}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": SchemaFor(t.Elem())}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{"type": "integer"}
	}
}

// Schemas describe the published shapes. Runtime validation additionally
// enforces semantic relationships, permissions, local assets, and limits.
func Schemas() map[string]map[string]any {
	result := map[string]map[string]any{}
	for version := 3; version <= 6; version++ {
		shape := SchemaFor(reflect.TypeFor[Spec]())
		if version < 6 {
			shape = SchemaFor(reflect.TypeFor[legacyV5TemplateSpec]())
		}
		props := shape["properties"].(map[string]any)
		props["schema_version"] = map[string]any{"const": version}
		props["kind"] = map[string]any{"enum": []string{"host", "container", "compose"}}
		if version < 5 {
			delete(props["host"].(map[string]any)["properties"].(map[string]any), "open_target")
		}
		if version == 3 {
			props["container"].(map[string]any)["properties"].(map[string]any)["release_policy"] = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"blocked_tag_prefixes": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}
		}
		key := "redeven-service-template-spec-v" + string(rune('0'+version)) + ".schema.json"
		shape["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		shape["title"] = "Redeven Service Template execution specification"
		result[key] = shape
	}
	for version := 1; version <= 3; version++ {
		shape := SchemaFor(reflect.TypeFor[Document]())
		props := shape["properties"].(map[string]any)
		props["schema_version"] = map[string]any{"const": version}
		props["kind"] = map[string]any{"const": Kind}
		specVersions := []int{6}
		if version == 1 {
			specVersions = []int{3}
		}
		if version == 2 {
			specVersions = []int{4, 5, 6}
		}
		alternatives := []any{}
		for _, v := range specVersions {
			alternatives = append(alternatives, map[string]any{"$ref": "redeven-service-template-spec-v" + string(rune('0'+v)) + ".schema.json"})
		}
		props["spec"] = map[string]any{"oneOf": alternatives}
		if version < 3 {
			required := []string{}
			for _, key := range shape["required"].([]string) {
				if key != "kind" && key != "default_locale" && key != "locales" {
					required = append(required, key)
				}
			}
			shape["required"] = required
			delete(props, "kind")
			delete(props, "default_locale")
			delete(props, "locales")
		}
		if version == 1 {
			props["version"] = props["recommended_version"]
			delete(props, "recommended_version")
			discovery := props["release_discovery"].(map[string]any)["properties"].(map[string]any)
			discovery["allow_prerelease"] = map[string]any{"type": "boolean"}
			discovery["allow_non_semver"] = map[string]any{"type": "boolean"}
		}
		shape["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		shape["title"] = "Redeven Service Template document"
		result["redeven-service-template-v"+string(rune('0'+version))+".schema.json"] = shape
	}
	result["redeven-service-template-localization.schema.json"] = SchemaFor(reflect.TypeFor[Localization]())
	return result
}
