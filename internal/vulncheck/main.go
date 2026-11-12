// Command vulncheck runs govulncheck with one guarded exception for GO-2026-6859.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const exception = "GO-2026-6859"

type report struct {
	Config  json.RawMessage `json:"config"`
	Error   json.RawMessage `json:"error"`
	Finding *struct {
		OSV   string `json:"osv"`
		Trace []struct {
			Function string `json:"function"`
		} `json:"trace"`
	} `json:"finding"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vulncheck:", err)
		os.Exit(1)
	}
}

func run() error {
	cmd := exec.Command("govulncheck", "-format=json", "./...")
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	var configured, exceptionFound, otherFindings bool
	moduleFindings := make(map[string]bool)
	for {
		var item report
		if err := decoder.Decode(&item); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decode govulncheck output: %w", err)
		}
		if item.Config != nil {
			configured = true
		}
		if item.Error != nil {
			return fmt.Errorf("govulncheck error: %s", item.Error)
		}
		if item.Finding == nil {
			continue
		}
		if len(item.Finding.Trace) == 0 || item.Finding.Trace[0].Function == "" {
			moduleFindings[item.Finding.OSV] = true
			continue // Module and package findings do not show a reachable symbol.
		}
		if item.Finding.OSV == exception {
			exceptionFound = true
			continue
		}
		fmt.Fprintln(os.Stderr, "reachable vulnerability:", item.Finding.OSV)
		otherFindings = true
	}
	if !configured {
		return fmt.Errorf("govulncheck returned no scan configuration")
	}
	var moduleIDs []string
	for id := range moduleFindings {
		if id == exception && exceptionFound {
			continue
		}
		moduleIDs = append(moduleIDs, id)
	}
	sort.Strings(moduleIDs)
	for _, id := range moduleIDs {
		fmt.Println(id + ": present in a module or package; no reachable symbol found")
	}
	if exceptionFound {
		if err := checkAffectedCalls(); err != nil {
			return err
		}
		fmt.Println(exception + ": accepted; scanned packages do not call the affected body-limit APIs")
	}
	if otherFindings {
		return fmt.Errorf("govulncheck found reachable vulnerabilities")
	}
	fmt.Println("No other reachable vulnerabilities found.")
	return nil
}

// checkAffectedCalls includes imported modules. This exception is no longer
// valid if any scanned package starts using either affected body-limit API.
func checkAffectedCalls() error {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", "./...")
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, dir := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if dir == "" || filepath.Clean(dir) == filepath.Join(root, "internal", "vulncheck") {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("MaxBytesReader")) || bytes.Contains(data, []byte("MaxBytesHandler")) {
				return fmt.Errorf("%s: body-limit API found; review %s and use a patched Go toolchain", path, exception)
			}
		}
	}
	return nil
}
