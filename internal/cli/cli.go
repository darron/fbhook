// Package cli implements commands over the public client and shared service.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/darron/fbhook"
	"io"
	"os"
	"time"
)

type BuildInfo struct{ Revision, State string }

const help = `fbhook — shared feedback intake and issue delivery

Usage:
  fbhook serve                         Run the service and background worker
  fbhook send --stdin [--key KEY]       Send a JSON envelope from stdin
  fbhook send --feedback TEXT          Send feedback with optional source fields
  fbhook keygen                        Generate base64 encryption/session keys
  fbhook version                       Show version, source SHA and build state

Send configuration:
  FBHOOK_ENDPOINT     Credential-bearing feedback URL (keep server-side)
  --app, --version, --environment, --actor, --title, --context JSON
  --key               Reuse for caller retries of the same exact payload

Service configuration and deployment: see docs/OPERATIONS.md.
`

func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, build BuildInfo) error {
	if len(args) == 0 {
		_, e := fmt.Fprint(out, help)
		return e
	}
	switch args[0] {
	case "help", "--help", "-h":
		_, e := fmt.Fprint(out, help)
		return e
	case "version":
		if build.Revision == "" {
			build.Revision = "unknown"
		}
		if build.State == "" {
			build.State = "unknown"
		}
		_, e := fmt.Fprintf(out, "fbhook %s sha=%s %s\n", fbhook.Version, build.Revision, build.State)
		return e
	case "keygen":
		if len(args) != 1 {
			return errors.New("keygen takes no arguments")
		}
		encryption := make([]byte, 32)
		session := make([]byte, 64)
		_, _ = rand.Read(encryption)
		_, _ = rand.Read(session)
		return json.NewEncoder(out).Encode(map[string]string{"FBHOOK_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(encryption), "FBHOOK_SESSION_KEY": base64.StdEncoding.EncodeToString(session)})
	case "send":
		return send(ctx, args[1:], in, out, errOut)
	case "serve":
		return serve(ctx, args[1:], out, errOut)
	default:
		return errors.New("unknown command; run fbhook help")
	}
}
func send(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	flags.SetOutput(errOut)
	var f fbhook.Feedback
	var fromStdin bool
	var key, rawContext string
	flags.BoolVar(&fromStdin, "stdin", false, "read JSON feedback from stdin")
	flags.StringVar(&key, "key", "", "idempotency key for repeated submissions")
	flags.StringVar(&f.Title, "title", "", "issue title")
	flags.StringVar(&f.Feedback, "feedback", "", "feedback text")
	flags.StringVar(&f.Source.App, "app", "", "source application")
	flags.StringVar(&f.Source.Version, "version", "", "application version")
	flags.StringVar(&f.Source.Environment, "environment", "", "application environment")
	flags.StringVar(&f.Source.Actor, "actor", "", "source actor, e.g. agent")
	flags.StringVar(&rawContext, "context", "", "application context as JSON")
	if e := flags.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("send does not accept positional arguments")
	}
	if fromStdin {
		if f.Feedback != "" || f.Title != "" || f.Source != (fbhook.Source{}) || rawContext != "" {
			return errors.New("--stdin cannot be combined with feedback fields")
		}
		if in == nil {
			return errors.New("stdin is required")
		}
		body, e := io.ReadAll(io.LimitReader(in, fbhook.MaxRequestBytes+1))
		if e != nil {
			return errors.New("cannot read feedback")
		}
		f, e = fbhook.Parse(body)
		if e != nil {
			return e
		}
	} else if rawContext != "" {
		if !json.Valid([]byte(rawContext)) {
			return errors.New("--context must be valid JSON")
		}
		f.Context = json.RawMessage(rawContext)
	}
	client, e := fbhook.NewClient(os.Getenv("FBHOOK_ENDPOINT"), nil)
	if e != nil {
		return errors.New("set FBHOOK_ENDPOINT to a valid feedback URL")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	receipt, e := client.Submit(requestCtx, f, key)
	if e != nil {
		return e
	}
	return json.NewEncoder(out).Encode(receipt)
}
