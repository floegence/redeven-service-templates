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
		for _, release := range plan.Releases {
			fmt.Printf("%s %s %s\n", release.Source.TemplateID, release.Version, release.Integrity)
			for _, platform := range release.Source.Platforms {
				fmt.Printf("%s %s\n", platform, release.Artifacts[platform])
			}
		}
		return
	}
	changed, err := releasewatcher.Apply(context.Background(), root, plan)
	if err != nil {
		fatalf("release update failed: %v", err)
	}
	if !changed {
		fmt.Println("Release catalog is already current")
		return
	}
	fmt.Println("Updated verified catalog releases")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
