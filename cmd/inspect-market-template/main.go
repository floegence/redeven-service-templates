// inspect-market-template reads a bounded snapshot from stdin and emits passive
// verified catalog evidence. It never executes source code or accesses a network.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/floegence/redeven-service-templates/template"
	"io"
	"os"
)

func main() {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 24*1024*1024+1))
	if err != nil || len(raw) > 24*1024*1024 {
		die("snapshot exceeds limit")
	}
	var snapshot template.Snapshot
	if err := template.DecodeJSON(raw, &snapshot); err != nil {
		die(err.Error())
	}
	item, err := template.InspectMarketTemplate(snapshot)
	if err != nil {
		die(err.Error())
	}
	if err := json.NewEncoder(os.Stdout).Encode(item); err != nil {
		die(err.Error())
	}
}
func die(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
