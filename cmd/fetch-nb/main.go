// fetch-nb authenticates against Fabric, resolves a workspace and notebook
// by displayName, and prints the parameters discovered by futils's
// parser. Used to validate the end-to-end flow against real notebooks
// before wiring the interactive CLI.
//
// Usage:
//
//	go run ./cmd/fetch-nb "<Workspace displayName>" "<Notebook displayName>"
//
// Optional flags:
//
//	-raw            dump the decoded .ipynb to stdout instead of parsed parameters
//	-profile        keychain profile name (default "default")
//	-run            actually trigger a RunNotebook job using discovered defaults
//	                (plus any -p overrides), then poll until completion
//	-p name=value   override a discovered parameter. Type is inherited from the
//	                notebook's declaration. Repeat the flag for multiple overrides.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// paramFlag collects repeated -p name=value arguments.
type paramFlag map[string]string

func (p paramFlag) String() string { return fmt.Sprintf("%v", map[string]string(p)) }
func (p paramFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("expected name=value, got %q", s)
	}
	p[k] = v
	return nil
}

func main() {
	raw := flag.Bool("raw", false, "print decoded .ipynb instead of parsed parameters")
	profile := flag.String("profile", "default", "keychain profile for token caching")
	run := flag.Bool("run", false, "submit a RunNotebook job using discovered defaults + -p overrides")
	overrides := paramFlag{}
	flag.Var(overrides, "p", "override parameter value (name=value, repeatable)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: fetch-nb [-raw] [-run] [-profile name] [-p k=v ...] <workspace> <notebook>")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}
	workspaceName := flag.Arg(0)
	notebookName := flag.Arg(1)

	token, err := fabric.GetAccessToken(*profile)
	check(err)

	fmt.Fprintf(os.Stderr, "Resolving workspace %q…\n", workspaceName)
	wsID, err := fabric.GetWorkspaceID(token, workspaceName)
	check(err)
	fmt.Fprintf(os.Stderr, "  → %s\n", wsID)

	fmt.Fprintf(os.Stderr, "Looking up notebook %q…\n", notebookName)
	item, err := fabric.FindNotebookByName(token, wsID, notebookName)
	check(err)
	fmt.Fprintf(os.Stderr, "  → %s\n", item.ID)

	fmt.Fprintln(os.Stderr, "Fetching definition (may take a few seconds)…")
	ipynb, err := fabric.GetNotebookIpynb(token, wsID, item.ID)
	check(err)

	if *raw {
		_, _ = os.Stdout.Write(ipynb)
		return
	}

	params, err := fabric.ParseParameters(ipynb)
	check(err)

	if !*run {
		if len(params) == 0 {
			fmt.Fprintln(os.Stderr, "(no parameters cell discovered — notebook has no Papermill-tagged cell)")
			return
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(params)
		return
	}

	// -run path: build JobInputs from overrides only, submit, poll.
	inputs, err := applyOverrides(params, overrides)
	check(err)

	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, "Submitting RunNotebook job with no parameter overrides (notebook uses its own defaults)")
	} else {
		fmt.Fprintln(os.Stderr, "Submitting RunNotebook job with overrides:")
		for _, in := range inputs {
			// Render values through json for proper quoting of strings vs
			// raw bool/int/float — same shape Fabric sees on the wire.
			v, _ := json.Marshal(in.Value)
			fmt.Fprintf(os.Stderr, "  %s = %s (%s)\n", in.Name, v, in.Type)
		}
	}

	instanceURL, err := fabric.RunNotebook(token, wsID, item.ID, inputs)
	check(err)
	fmt.Fprintf(os.Stderr, "Job started: %s\n", instanceURL)

	waitForJob(token, instanceURL)
}

// applyOverrides builds the JobInput list that will be sent to Fabric. Only
// user-specified overrides are included — parameters the user didn't touch
// are left off the payload entirely, letting the notebook use its own
// declared defaults. This matches Fabric's semantics ("per-run inputs to
// tailor this invocation") and side-steps a server-side quirk where
// empty-string Text values are rejected as "Value field is required".
//
// Unknown override names are rejected so typos surface before the API call.
func applyOverrides(params []fabric.Parameter, overrides map[string]string) ([]fabric.JobInput, error) {
	byName := make(map[string]fabric.Parameter, len(params))
	for _, p := range params {
		byName[p.Name] = p
	}
	for k := range overrides {
		if _, ok := byName[k]; !ok {
			return nil, fmt.Errorf("override %q does not match any declared parameter", k)
		}
	}

	inputs := make([]fabric.JobInput, 0, len(overrides))
	for _, p := range params { // iterate params (not map) to preserve notebook order
		raw, ok := overrides[p.Name]
		if !ok {
			continue
		}
		coerced, err := coerce(raw, p.Type)
		if err != nil {
			return nil, fmt.Errorf("-p %s=%q: %w", p.Name, raw, err)
		}
		inputs = append(inputs, fabric.JobInput{Name: p.Name, Value: coerced, Type: p.Type})
	}
	return inputs, nil
}

// coerce turns a string from the command line into a typed value matching
// the declared parameter type. Strings are passed through verbatim — which
// is exactly why commas in a string value are safe.
func coerce(raw, typ string) (any, error) {
	switch typ {
	case fabric.TypeString:
		return raw, nil
	case fabric.TypeBool:
		return strconv.ParseBool(raw)
	case fabric.TypeInt:
		return strconv.ParseInt(raw, 10, 64)
	case fabric.TypeFloat:
		return strconv.ParseFloat(raw, 64)
	}
	return nil, fmt.Errorf("unsupported type %q", typ)
}

// waitForJob polls a job instance every 5s until it reaches a terminal
// state, printing status transitions along the way.
func waitForJob(token, instanceURL string) {
	var last string
	for {
		status, err := fabric.GetJobInstance(token, instanceURL)
		check(err)
		if status.Status != last {
			fmt.Fprintf(os.Stderr, "  status: %s\n", status.Status)
			last = status.Status
		}
		switch status.Status {
		case "Completed":
			fmt.Fprintln(os.Stderr, "✅ job completed")
			return
		case "Failed":
			enc := json.NewEncoder(os.Stderr)
			enc.SetIndent("    ", "  ")
			_ = enc.Encode(status)
			os.Exit(1)
		case "Cancelled", "Deduped":
			fmt.Fprintf(os.Stderr, "⚠️  terminal status: %s\n", status.Status)
			return
		}
		time.Sleep(5 * time.Second)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
