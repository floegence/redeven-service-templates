package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/floegence/redeven-service-templates/internal/cataloggen"
)

func main() {
	verify := flag.Bool("verify", false, "verify committed generated files")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("unexpected arguments")
	}
	root, err := os.Getwd()
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}
	if err := cataloggen.HasForbiddenCompatibilityCode(root); err != nil {
		fatalf("validate source boundary: %v", err)
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
