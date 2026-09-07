package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/floegence/redeven-service-templates/internal/releasewatcher"
)

func main() {
	check := flag.Bool("check", false, "discover and print verified releases without changing files")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("unexpected arguments")
	}
	root, err := os.Getwd()
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}
	plan, err := releasewatcher.DiscoverRepository(context.Background(), root, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		fatalf("release discovery failed: %v", err)
	}
	if *check {
		fmt.Printf("Host %s (%s)\nContainer %s\n", plan.HostVersion, plan.HostIntegrity, plan.ContainerVersion)
		for platform, artifact := range plan.ContainerArtifacts {
			fmt.Printf("%s %s\n", platform, artifact)
		}
		return
	}
	changed, err := releasewatcher.Apply(root, plan)
	if err != nil {
		fatalf("release update failed: %v", err)
	}
	if !changed {
		fmt.Println("Release catalog is already current")
		return
	}
	fmt.Printf("Updated DeepSeek Harness releases: host=%s container=%s\n", plan.HostVersion, plan.ContainerVersion)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
