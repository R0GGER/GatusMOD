package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gatusmod/internal/app"
	"gatusmod/internal/config"
	"gatusmod/internal/probe"
	"gatusmod/internal/proxy"

	"golang.org/x/crypto/bcrypt"
)

const (
	configPathEnvVar = "GATUSMOD_CONFIG_PATH"
	dataPathEnvVar   = "GATUSMOD_DATA_PATH"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hash-password":
			os.Exit(hashPassword(os.Args[2:]))
		case "healthcheck":
			os.Exit(healthcheck())
		case "probe":
			os.Exit(runProbe(os.Args[2:]))
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (available: hash-password, healthcheck, probe)\n", os.Args[1])
			os.Exit(2)
		}
	}
	if err := serve(); err != nil {
		slog.Error("GatusMOD stopped", "error", err)
		os.Exit(1)
	}
}

func serve() error {
	path := os.Getenv(configPathEnvVar)
	if path == "" {
		path = "/config"
	}
	watcher, err := config.NewWatcher(path)
	if err != nil {
		return fmt.Errorf("loading configuration from %s: %w", path, err)
	}
	cfg := watcher.Get()
	application, err := app.New(watcher, envOr(dataPathEnvVar, "/data"))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go watcher.Run(ctx, 30*time.Second)
	go application.Syncer.Run(ctx)
	go application.Probes.Run(ctx)
	go proxy.SelfTest(ctx, application, application.Tracker)

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           application,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("GatusMOD listening", "address", cfg.Listen, "upstream", cfg.Upstream, "config", path)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// hashPassword prints a bcrypt hash with every $ doubled, because Gatus and GatusMOD expand $VAR in the configuration.
func hashPassword(args []string) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: gatusmod hash-password <password>")
		return 2
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(args[0]), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(strings.ReplaceAll(string(hash), "$", "$$"))
	return 0
}

// runProbe checks one URL once, e.g. `gatusmod probe ntp://pool.ntp.org "[OFFSET] < 100ms"`.
// Without conditions the defaults of the probe type are used; an iperf3 URL with ?bandwidth= runs the test right away.
func runProbe(args []string) int {
	if len(args) == 0 || !config.IsProbeURL(args[0]) || config.SchemeType(args[0]) == config.TypePush {
		fmt.Fprintln(os.Stderr, "usage: gatusmod probe <iperf3|ntp|snmp|smtp|smtps|postgres|mysql|redis|mongodb URL> [condition ...]")
		return 2
	}
	conditions := args[1:]
	if len(conditions) == 0 {
		conditions = strings.Split(config.DefaultConditions(config.SchemeType(args[0])), "\n")
	}
	if err := probe.Validate(args[0], conditions); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	_, bandwidth := probe.BandwidthInterval(args[0])
	res := probe.Check(context.Background(), args[0], conditions, probe.Options{Bandwidth: bandwidth})
	fmt.Printf("url:        %s\nconditions: %s\nvalues:     %s\nduration:   %s\n",
		probe.Redact(args[0]), strings.Join(conditions, "; "), res.Values, res.Duration.Round(time.Millisecond))
	if !res.Success {
		fmt.Printf("result:     down (%s)\n", res.Error)
		return 1
	}
	fmt.Println("result:     up")
	return 0
}

func healthcheck() int {
	addr := config.DefaultListen
	if cfg, err := config.Load(envOr(configPathEnvVar, "/config")); err == nil {
		addr = cfg.Listen
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/mod/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
