package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/floegence/redeven-service-templates/internal/cataloggen"
	"github.com/floegence/redeven-service-templates/internal/registrycheck"
)

func main() {
	verify := flag.Bool("verify", false, "verify committed generated files")
	checkRegistry := flag.Bool("check-registry", false, "verify recommended OCI releases against their Registries")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("unexpected arguments")
	}
	if !*verify && !*checkRegistry {
		fatalf("catalog generation requires --check-registry")
	}
	root, err := os.Getwd()
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}
	if err := cataloggen.HasForbiddenCompatibilityCode(root); err != nil {
		fatalf("validate source boundary: %v", err)
	}
	if *checkRegistry {
		if err := registrycheck.Check(context.Background(), root, registrycheck.Options{Client: &http.Client{Timeout: 20 * time.Second}}); err != nil {
			fatalf("registry preflight failed: %v", err)
		}
	}
	output, err := cataloggen.Generate(root)
	if err != nil {
		fatalf("build catalog: %v", err)
	}
	if *verify {
		err = cataloggen.Verify(root, output)
	} else {
		err = cataloggen.Write(root, output)
	}
	if err != nil {
		fatalf("write catalog: %v", err)
	}
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", arguments...)
	os.Exit(1)
}
