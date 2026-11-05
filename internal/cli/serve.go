package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	ghissues "github.com/darron/fbhook/issues/github"
	"github.com/darron/fbhook/issues/linear"
	"github.com/darron/fbhook/service"
	"github.com/darron/fbhook/web"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func keyFromEnv(name string, size int) ([]byte, error) {
	b, e := base64.StdEncoding.DecodeString(os.Getenv(name))
	if e != nil || len(b) != size {
		return nil, fmt.Errorf("%s must be base64 encoding of %d bytes; use fbhook keygen", name, size)
	}
	return b, nil
}
func serve(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(errOut)
	listen := flags.String("listen", ":8080", "HTTP listen address")
	if e := flags.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("serve does not accept positional arguments")
	}
	encryption, e := keyFromEnv("FBHOOK_ENCRYPTION_KEY", 32)
	if e != nil {
		return e
	}
	session, e := keyFromEnv("FBHOOK_SESSION_KEY", 64)
	if e != nil {
		return e
	}
	interval := 15 * time.Second
	if raw := os.Getenv("FBHOOK_POLL_INTERVAL"); raw != "" {
		interval, e = time.ParseDuration(raw)
		if e != nil || interval < time.Second || interval > time.Hour {
			return errors.New("FBHOOK_POLL_INTERVAL must be between 1s and 1h")
		}
	}
	store, closeStore, e := storageFromEnv(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = closeStore() }()
	svc, e := service.New(store, service.Options{EncryptionKey: encryption, PollInterval: interval, Providers: map[string]service.Provider{"github": ghissues.New(nil, ""), "linear": linear.New(nil, "")}})
	if e != nil {
		return e
	}
	handler, e := web.New(svc, web.Options{BaseURL: strings.TrimRight(os.Getenv("FBHOOK_BASE_URL"), "/"), OAuthClientID: os.Getenv("GITHUB_CLIENT_ID"), OAuthClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"), AllowedUsers: strings.Split(os.Getenv("FBHOOK_GITHUB_USERS"), ","), SessionKey: session, TrustedProxyCIDRs: splitNonempty(os.Getenv("FBHOOK_TRUSTED_PROXY_CIDRS")), ClientIPHeader: os.Getenv("FBHOOK_CLIENT_IP_HEADER")})
	if e != nil {
		return e
	}
	listener, e := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if e != nil {
		return errors.New("cannot open HTTP listener")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024, ErrorLog: log.New(io.Discard, "", 0)}
	workerDone := make(chan struct{}, 1)
	go func() { _ = svc.Run(runCtx); workerDone <- struct{}{} }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	_, _ = fmt.Fprintln(out, "fbhook service started; worker enabled")
	select {
	case <-ctx.Done():
	case e = <-serverDone:
		if !errors.Is(e, http.ErrServerClosed) {
			cancel()
			_ = server.Close()
			<-workerDone
			return errors.New("HTTP server stopped unexpectedly")
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = server.Close()
	}
	<-workerDone
	return shutdownErr
}

func splitNonempty(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
