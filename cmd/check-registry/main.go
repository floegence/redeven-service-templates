package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/floegence/redeven-service-templates/internal/registrycheck"
)

func main() {
	timeout := flag.Duration("timeout", 20*time.Second, "maximum time for each Registry preflight run")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("unexpected arguments")
	}
	root, err := os.Getwd()
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}
	client := &http.Client{Timeout: *timeout}
	if err := registrycheck.Check(context.Background(), root, registrycheck.Options{Client: client}); err != nil {
		fatalf("registry preflight failed: %v", err)
	}
	fmt.Println("Registry preflight passed")
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", arguments...)
	os.Exit(1)
}
