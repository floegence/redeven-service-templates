package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/floegence/redeven-service-templates/template"
)

func main() {
	verify := flag.Bool("verify", false, "Verify committed contracts")
	flag.Parse()
	write := func(name string, raw []byte) {
		if *verify {
			old, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(old, raw) {
				panic("stale generated contract: " + name)
			}
			return
		}
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(name, raw, 0644); err != nil {
			panic(err)
		}
	}
	for name, schema := range template.Schemas() {
		raw, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			panic(err)
		}
		write("sdk/schema/"+name, append(raw, '\n'))
	}
	definitions := map[string]string{}
	var ts func(reflect.Type) string
	ts = func(t reflect.Type) string {
		if t == reflect.TypeFor[json.RawMessage]() {
			return "Spec"
		}
		if t == reflect.TypeFor[[]byte]() {
			return "string"
		}
		if t.Kind() == reflect.Pointer {
			return ts(t.Elem())
		}
		switch t.Kind() {
		case reflect.Struct:
			if _, ok := definitions[t.Name()]; !ok {
				definitions[t.Name()] = ""
				var b strings.Builder
				fmt.Fprintf(&b, "export interface %s {\n", t.Name())
				for i := 0; i < t.NumField(); i++ {
					f := t.Field(i)
					tag := strings.Split(f.Tag.Get("json"), ",")
					if tag[0] == "" || tag[0] == "-" {
						continue
					}
					optional := ""
					if len(tag) > 1 {
						optional = "?"
					}
					fmt.Fprintf(&b, "  %s%s: %s;\n", tag[0], optional, ts(f.Type))
				}
				b.WriteString("}\n")
				definitions[t.Name()] = b.String()
			}
			return t.Name()
		case reflect.Map:
			return "Record<string, " + ts(t.Elem()) + ">"
		case reflect.Slice:
			return "Array<" + ts(t.Elem()) + ">"
		case reflect.String:
			if t.Name() == "Deployment" {
				return "'host' | 'container' | 'compose'"
			}
			return "string"
		case reflect.Bool:
			return "boolean"
		default:
			return "number"
		}
	}
	for _, t := range template.ContractTypes() {
		ts(t)
	}
	names := []string{}
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	out.WriteString("// Generated from the published Go contracts. Do not edit.\n")
	for _, name := range names {
		out.WriteString(definitions[name])
		out.WriteString("\n")
	}
	out.WriteString(`export const FILENAME: 'redeven-service-template.json';
export const MAX_FILE_BYTES: number;
export const MAX_DIRECTORY_BYTES: number;
export const MAX_FILES: number;
export class TemplateSourceError extends Error { readonly code: string; }
export interface AcquisitionOptions {
  signal?: AbortSignal;
  fetch?: typeof globalThis.fetch;
  onProgress?: (progress: {phase: 'downloading'; files: number; bytes: number}) => void;
}
export function sourceDigest(files: File[]): string;
export function resolveSource(input: GitSource, token?: string, options?: AcquisitionOptions): Promise<ResolvedSource>;
export function discoverSources(input: GitSource, token?: string, options?: AcquisitionOptions): Promise<SourceCatalog>;
export function captureSource(input: GitSource, token?: string, options?: AcquisitionOptions): Promise<Snapshot>;
`)
	write("sdk/index.d.ts", []byte(out.String()))
}
