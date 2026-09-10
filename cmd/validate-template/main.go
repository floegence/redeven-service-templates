// validate-template validates source files without executing or rewriting them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/floegence/redeven-service-templates/template"
)

func main() {
	current := flag.Bool("current", false, "require the current branded author format")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: validate-template [--current] <template-directory>")
		os.Exit(2)
	}
	files, err := template.ReadDirectory(flag.Arg(0))
	if err != nil {
		die(err)
	}
	parsed, err := template.Read(files)
	if err != nil {
		die(err)
	}
	if *current && (parsed.SourceDocumentVersion != template.DocumentVersion || parsed.SourceSpecVersion != template.SpecVersion) {
		die(fmt.Errorf("author new templates using %s document v%d and execution v%d", template.Filename, template.DocumentVersion, template.SpecVersion))
	}
	result := map[string]any{"valid": true, "kind": template.Kind, "template_id": parsed.Document.TemplateID, "source_document_version": parsed.SourceDocumentVersion, "effective_document_version": template.DocumentVersion, "source_spec_version": parsed.SourceSpecVersion, "effective_spec_version": template.SpecVersion, "default_locale": parsed.Document.DefaultLocale, "sha256": parsed.SHA256}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		die(err)
	}
}
func die(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
