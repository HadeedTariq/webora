package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ── Constants ────────────────────────────────────────────────────────────────

const (
	defaultweboraBin = "webora"
	defaultDepth     = 3
	defaultWorkers   = 8

	// statsInterval controls how often the live progress line is printed.
	statsInterval = 15 * time.Second

	// killGracePeriod is how long we wait for webora to honour SIGTERM
	// before escalating to SIGKILL.
	killGracePeriod = 5 * time.Second

	// Output file names — both opened in append mode so re-runs accumulate.
	fileSitemap  = "sitemap_paths.txt"
	fileSecurity = "security_surface.txt"

	// ANSI escape codes — used only for interactive terminal output,
	// never written to the output files themselves.
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

// sensitiveKeywords are path segments that indicate a security-relevant endpoint.
// Any URL whose lower-case form contains one of these strings is routed to
// security_surface.txt instead of (or in addition to) sitemap_paths.txt.
var sensitiveKeywords = []string{
	"admin", "administrator",
	"api", "v1", "v2", "v3",
	"config", "configuration",
	"checkout", "payment", "billing",
	"login", "signin", "auth", "oauth", "sso",
	"secret", "private", "internal",
	"debug", "trace", "profiler", "pprof",
	"backup", "dump", "export",
	"passwd", "password", "credential",
	"dashboard", "manage", "management",
	"shell", "console", "terminal",
	".env", ".git", ".svn", ".htaccess",
}

// ── URL Category ─────────────────────────────────────────────────────────────

// URLCategory classifies a discovered URL for routing purposes.
type URLCategory uint8

const (
	// CategorySitemap routes the URL to sitemap_paths.txt.
	CategorySitemap URLCategory = iota
	// CategorySecurity routes the URL to security_surface.txt.
	CategorySecurity
)

// ── Config ───────────────────────────────────────────────────────────────────

// Config holds all runtime parameters parsed from CLI flags.
// Every webora flag is surfaced here so the operator has full control
// without touching the source.
type Config struct {
	TargetURL  string
	weboraBin  string
	Depth      int
	Workers    int
	Delay      string
	ScanJS     bool
	ScanCSS    bool
	Subdomains bool
	NoHEAD     bool
	Brute      bool
	DirsOnly   bool
	RobotsMode string // "respect" | "ignore"
}

func parseConfig() (*Config, error) {
	cfg := &Config{}

	flag.StringVar(&cfg.TargetURL, "target", "", "Target URL to crawl (required)")
	flag.StringVar(&cfg.weboraBin, "bin", defaultweboraBin, "Path to webora binary")
	flag.IntVar(&cfg.Depth, "depth", defaultDepth, "Maximum crawl depth from the base URL")
	flag.IntVar(&cfg.Workers, "workers", defaultWorkers, "Concurrent crawl worker count")
	flag.StringVar(&cfg.Delay, "delay", "", "Polite delay between requests, e.g. 200ms or 1s")
	flag.BoolVar(&cfg.ScanJS, "js", true, "Scan JavaScript files for embedded URLs")
	flag.BoolVar(&cfg.ScanCSS, "css", false, "Scan CSS files for embedded URLs")
	flag.BoolVar(&cfg.Subdomains, "subs", false, "Follow links that resolve to subdomains")
	flag.BoolVar(&cfg.NoHEAD, "nohead", false, "Skip HEAD pre-flight — infer type from extension only")
	flag.BoolVar(&cfg.Brute, "brute", false, "Enable brute-force tag attribute scanning in HTML")
	flag.BoolVar(&cfg.DirsOnly, "dirs-only", false, "Emit only directory-style paths (no file extensions)")
	flag.StringVar(&cfg.RobotsMode, "robots", "respect", "robots.txt policy: respect | ignore")

	flag.Parse()

	if cfg.TargetURL == "" {
		flag.Usage()
		return nil, fmt.Errorf("--target is required")
	}

	return cfg, nil
}

// args translates Config into the webora subprocess argument slice.
// Keeping argument construction here makes the subprocess call site clean.
func (c *Config) args() []string {
	a := []string{
		"-depth", fmt.Sprint(c.Depth),
		"-workers", fmt.Sprint(c.Workers),
	}

	if c.Delay != "" {
		a = append(a, "-delay", c.Delay)
	}
	if c.ScanJS {
		a = append(a, "-js")
	}
	if c.ScanCSS {
		a = append(a, "-css")
	}
	if c.Subdomains {
		a = append(a, "-subs")
	}
	if c.NoHEAD {
		a = append(a, "-nohead")
	}
	if c.Brute {
		a = append(a, "-brute")
	}
	if c.DirsOnly {
		a = append(a, "-dirs", "only")
	}
	if c.RobotsMode == "ignore" {
		a = append(a, "-robots", "ignore")
	}

	// The target URL must be the final argument.
	a = append(a, c.TargetURL)

	return a
}

// ── FileStore ─────────────────────────────────────────────────────────────────

// FileStore is a thread-safe, append-only dual-file storage engine.
//
// Design decisions:
//   - A single Mutex guards both file handles so writes from concurrent
//     goroutines never interleave partial lines.
//   - Atomic counters for stats are read without holding the Mutex, which
//     avoids contention on the hot-path ticker goroutine.
//   - Each file is opened with O_APPEND so the OS guarantees atomicity for
//     writes up to PIPE_BUF (typically 4 KB), but we keep the Mutex anyway
//     because a URL + newline can exceed PIPE_BUF on embedded systems.
type FileStore struct {
	mu       sync.Mutex
	sitemap  *os.File
	security *os.File

	// Counters are incremented atomically so Stats() can be called
	// from any goroutine without holding mu.
	sitemapCount  atomic.Int64
	securityCount atomic.Int64
}

// NewFileStore opens (or creates) both output files in append mode.
// Caller must call Close() when done.
func NewFileStore(sitemapPath, securityPath string) (*FileStore, error) {
	const flags = os.O_CREATE | os.O_APPEND | os.O_WRONLY
	const perm = 0o644

	sm, err := os.OpenFile(sitemapPath, flags, perm)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", sitemapPath, err)
	}

	sec, err := os.OpenFile(securityPath, flags, perm)
	if err != nil {
		_ = sm.Close()
		return nil, fmt.Errorf("open %s: %w", securityPath, err)
	}

	return &FileStore{sitemap: sm, security: sec}, nil
}

// Write routes url to the correct output file and updates the relevant counter.
// It is safe to call from multiple goroutines simultaneously.
func (fs *FileStore) Write(cat URLCategory, url string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	switch cat {
	case CategorySecurity:
		// Security entries carry a UTC timestamp for incident correlation.
		line := fmt.Sprintf("[%s] %s\n", time.Now().UTC().Format(time.RFC3339), url)
		if _, err := io.WriteString(fs.security, line); err != nil {
			return fmt.Errorf("write security: %w", err)
		}
		fs.securityCount.Add(1)

	default:
		if _, err := fmt.Fprintln(fs.sitemap, url); err != nil {
			return fmt.Errorf("write sitemap: %w", err)
		}
		fs.sitemapCount.Add(1)
	}

	return nil
}

// Stats returns current counts for both files without acquiring the file Mutex.
func (fs *FileStore) Stats() (sitemap, security int64) {
	return fs.sitemapCount.Load(), fs.securityCount.Load()
}

// Close flushes OS buffers and closes both files.
// Safe to call multiple times (subsequent calls are no-ops on closed files).
func (fs *FileStore) Close() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	for _, f := range []*os.File{fs.sitemap, fs.security} {
		_ = f.Sync() // flush OS write buffer to disk
		_ = f.Close()
	}
}

// ── Classifier ───────────────────────────────────────────────────────────────

// Classifier applies keyword matching rules to categorize URLs.
// It intentionally has no mutable state after construction, making it
// safe for concurrent use without any synchronisation.
type Classifier struct {
	keywords []string
}

// NewClassifier returns a Classifier pre-loaded with the given keyword list.
func NewClassifier(keywords []string) *Classifier {
	// Copy the slice so the caller cannot mutate our internal state.
	kw := make([]string, len(keywords))
	copy(kw, keywords)
	return &Classifier{keywords: kw}
}

// Classify returns the URL's category and the first matched keyword.
// The empty string is returned as the keyword for non-security URLs.
func (cl *Classifier) Classify(url string) (URLCategory, string) {
	lower := strings.ToLower(url)

	for _, kw := range cl.keywords {
		if strings.Contains(lower, kw) {
			return CategorySecurity, kw
		}
	}

	return CategorySitemap, ""
}

// ── Monitor ──────────────────────────────────────────────────────────────────

// Monitor is the central orchestrator. It owns the webora subprocess lifecycle,
// the pipe goroutines, and all business logic that connects them.
//
// Concurrency model:
//
//	main goroutine → Run() → starts subprocess
//	                       → goroutine: drainStdout  (reads URL lines)
//	                       → goroutine: drainStderr  (forwards diagnostics)
//	                       → goroutine: runStatsTicker (prints progress)
//	                       → wg.Wait() for drain goroutines only
//	                       → cmd.Wait() for process exit
type Monitor struct {
	cfg        *Config
	store      *FileStore
	classifier *Classifier
	log        *slog.Logger

	cmd       *exec.Cmd
	startTime time.Time
}

// NewMonitor constructs a Monitor with all dependencies injected.
// All fields are required; passing nil causes a panic at the relevant call site.
func NewMonitor(cfg *Config, store *FileStore, cl *Classifier, log *slog.Logger) *Monitor {
	return &Monitor{cfg: cfg, store: store, classifier: cl, log: log}
}

// Run starts the webora subprocess, attaches to its streams, and blocks until
// either webora exits naturally or the ctx is cancelled (e.g. Ctrl+C).
func (m *Monitor) Run(ctx context.Context) error {
	// exec.Command (not CommandContext) gives us full control over when and how
	// the process is terminated — we want SIGTERM → grace period → SIGKILL,
	// not the immediate SIGKILL that CommandContext sends on cancellation.
	m.cmd = exec.Command(m.cfg.weboraBin, m.cfg.args()...)
	m.startTime = time.Now()

	m.log.Info("launching webora subprocess",
		slog.String("binary", m.cfg.weboraBin),
		slog.String("target", m.cfg.TargetURL),
		slog.Int("depth", m.cfg.Depth),
		slog.Int("workers", m.cfg.Workers),
		slog.Any("args", m.cfg.args()),
	)

	stdoutPipe, err := m.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := m.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("start webora: %w", err)
	}

	m.log.Info("webora subprocess running", slog.Int("pid", m.cmd.Process.Pid))

	// The stats ticker runs independently — it does not hold up wg.Wait().
	// tickCancel stops it as soon as the pipe goroutines finish draining.
	tickCtx, tickCancel := context.WithCancel(ctx)
	go m.runStatsTicker(tickCtx)

	// Drain both pipes concurrently. wg tracks only these two goroutines.
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		m.drainStdout(stdoutPipe)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		m.drainStderr(stderrPipe)
	}()

	// Block until both pipes reach EOF — this happens when webora exits.
	wg.Wait()
	tickCancel() // stop the ticker goroutine immediately; no more data is coming

	// Reap the subprocess. cmd.Wait() must be called exactly once.
	if err := m.cmd.Wait(); err != nil {
		// A non-zero exit code after context cancellation is expected:
		// we sent SIGTERM, so webora exits with a signal error.
		if ctx.Err() != nil {
			m.log.Info("webora terminated by signal — expected non-zero exit")
			return nil
		}

		return fmt.Errorf("webora exited with error: %w", err)
	}

	return nil
}

// drainStdout reads webora's URL stream line-by-line using a buffered scanner
// so that each URL is processed as soon as it appears on the pipe — no
// buffering delay, no waiting for the full crawl to complete.
func (m *Monitor) drainStdout(r io.Reader) {
	// 512 KB per token handles edge-case URLs with very long query strings.
	const maxTokenSize = 512 * 1024

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, maxTokenSize), maxTokenSize)

	for sc.Scan() {
		url := strings.TrimSpace(sc.Text())
		if url == "" {
			continue
		}

		m.processURL(url)
	}

	if err := sc.Err(); err != nil {
		m.log.Error("stdout scanner error", slog.String("err", err.Error()))
	}
}

// drainStderr captures webora's diagnostic output and routes each line through
// the structured logger so operators see rate-limit warnings, HTTP errors, and
// robots.txt parse failures alongside the application's own log stream.
func (m *Monitor) drainStderr(r io.Reader) {
	sc := bufio.NewScanner(r)

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}

		// webora prefixes its internal messages with [-] for warnings
		// and [!] for hard errors. Map those to the appropriate slog level.
		switch {
		case strings.HasPrefix(line, "[!]"):
			m.log.Error("webora diagnostic", slog.String("msg", line))
		case strings.HasPrefix(line, "[-]"):
			m.log.Warn("webora diagnostic", slog.String("msg", line))
		default:
			m.log.Debug("webora stderr", slog.String("msg", line))
		}
	}
}

// processURL classifies a single URL, writes it to the correct file, and
// renders a colour-coded line to the terminal.
func (m *Monitor) processURL(url string) {
	cat, keyword := m.classifier.Classify(url)

	switch cat {
	case CategorySecurity:
		// Red + bold to make security hits immediately visible in the scroll.
		fmt.Printf(
			"%s%s[SECURITY]%s %-70s %s← %s%s\n",
			ansiBold, ansiRed, ansiReset,
			url,
			ansiYellow, keyword, ansiReset,
		)
		m.log.Info("security surface endpoint",
			slog.String("url", url),
			slog.String("keyword", keyword),
		)

	default:
		fmt.Printf("%s[SITEMAP] %s%s\n", ansiGreen, ansiReset, url)
	}

	if err := m.store.Write(cat, url); err != nil {
		m.log.Error("store write failed",
			slog.String("url", url),
			slog.String("err", err.Error()),
		)
	}
}

// runStatsTicker prints a live progress snapshot every statsInterval.
// It exits cleanly when ctx is cancelled (called after wg.Wait() in Run).
func (m *Monitor) runStatsTicker(ctx context.Context) {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm, sec := m.store.Stats()
			m.log.Info("live progress",
				slog.Int64("sitemap_urls", sm),
				slog.Int64("security_hits", sec),
				slog.Int64("total", sm+sec),
				slog.String("elapsed", time.Since(m.startTime).Round(time.Second).String()),
			)
		case <-ctx.Done():
			return
		}
	}
}

// Kill sends SIGTERM to the webora subprocess and then starts a background
// goroutine that escalates to SIGKILL if the process does not exit within
// killGracePeriod. This two-phase approach lets webora flush its own buffers.
func (m *Monitor) Kill() {
	if m.cmd == nil || m.cmd.Process == nil {
		return
	}

	pid := m.cmd.Process.Pid
	m.log.Info("sending SIGTERM to webora subprocess", slog.Int("pid", pid))

	if err := m.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// SIGTERM failed (process may have already exited) — fall back to SIGKILL.
		m.log.Warn("SIGTERM delivery failed, escalating immediately",
			slog.Int("pid", pid),
			slog.String("err", err.Error()),
		)
		_ = m.cmd.Process.Kill()
		return
	}

	// Escalation goroutine — runs independently and checks whether webora
	// has exited before sending SIGKILL. Signal(0) is a POSIX convention
	// for "does this process exist?" — it sends no actual signal.
	go func() {
		time.Sleep(killGracePeriod)

		if err := m.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			// Process is gone — SIGTERM worked. Nothing to do.
			return
		}

		m.log.Warn("grace period expired, sending SIGKILL", slog.Int("pid", pid))
		_ = m.cmd.Process.Kill()
	}()
}

// PrintSummary renders a final structured report to stdout after the crawl ends.
func (m *Monitor) PrintSummary() {
	sm, sec := m.store.Stats()
	elapsed := time.Since(m.startTime).Round(time.Millisecond)

	fmt.Printf("\n%s%s── Crawl Complete ─────────────────────────────%s\n", ansiBold, ansiCyan, ansiReset)
	fmt.Printf("  Target      : %s%s%s\n", ansiBold, m.cfg.TargetURL, ansiReset)
	fmt.Printf("  Duration    : %s\n", elapsed)
	fmt.Printf("  Sitemap URLs: %s%d%s  →  %s\n", ansiGreen, sm, ansiReset, fileSitemap)
	fmt.Printf("  Security IDs: %s%d%s  →  %s\n", ansiRed, sec, ansiReset, fileSecurity)
	fmt.Printf("  Total URLs  : %d\n", sm+sec)
	fmt.Printf("%s───────────────────────────────────────────────%s\n\n", ansiCyan, ansiReset)
}

// ── Banner ───────────────────────────────────────────────────────────────────

func printBanner(cfg *Config) {
	fmt.Printf("%s%s", ansiBold, ansiCyan)
	fmt.Println("╔═══════════════════════════════════════════════╗")
	fmt.Println("║        Live Security Monitor  v1.0.0          ║")
	fmt.Println("║        Powered by  s0rg/webora               ║")
	fmt.Println("╚═══════════════════════════════════════════════╝")
	fmt.Printf("%s", ansiReset)
	fmt.Printf("  Target   : %s%s%s\n", ansiBold, cfg.TargetURL, ansiReset)
	fmt.Printf("  Depth    : %d\n", cfg.Depth)
	fmt.Printf("  Workers  : %d\n", cfg.Workers)
	fmt.Printf("  Scan JS  : %v\n", cfg.ScanJS)
	fmt.Printf("  Scan CSS : %v\n", cfg.ScanCSS)
	fmt.Printf("  Robots   : %s\n", cfg.RobotsMode)
	fmt.Printf("  Output   : %s  |  %s\n\n", fileSitemap, fileSecurity)
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	// Structured logger writes exclusively to stderr.
	// This ensures logger output never collides with webora's stdout stream
	// or with the URL lines we print to stdout for the operator.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// ── 1. Parse and validate configuration ──────────────────────────────────

	cfg, err := parseConfig()
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}

	// ── 2. Verify the webora binary is reachable ─────────────────────────────

	binPath, err := exec.LookPath(cfg.weboraBin)
	if err != nil {
		log.Fatalf(
			"webora binary %q not found in PATH.\n"+
				"Install it with:\n"+
				"  go install github.com/HadeedTariq/webora/cmd/webora@latest\n",
			cfg.weboraBin,
		)
	}

	logger.Info("webora binary located", slog.String("path", binPath))

	// ── 3. Initialise storage ─────────────────────────────────────────────────

	store, err := NewFileStore(fileSitemap, fileSecurity)
	if err != nil {
		log.Fatalf("storage initialisation failed: %v\n", err)
	}
	defer store.Close() // guaranteed flush + close on any exit path

	// ── 4. Wire up the component graph ───────────────────────────────────────

	classifier := NewClassifier(sensitiveKeywords)
	monitor := NewMonitor(cfg, store, classifier, logger)

	// ── 5. Set up context and OS signal handling ──────────────────────────────

	// ctx is the single cancellation root for the entire application.
	// Cancelling it causes the stats ticker to exit; it does NOT kill the
	// subprocess directly (that is monitor.Kill()'s responsibility).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Buffer the channel so the signal goroutine never blocks even if the
	// main goroutine is momentarily busy inside wg.Wait().
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigCh

		fmt.Printf(
			"\n%s%s[SIGNAL] %v received — initiating graceful shutdown...%s\n",
			ansiBold, ansiYellow, sig, ansiReset,
		)

		// Phase 1: ask webora to stop cleanly.
		monitor.Kill()

		// Phase 2: cancel the application context so ancillary goroutines
		// (stats ticker) also stop without waiting for their next tick.
		cancel()
	}()

	// ── 6. Run ───────────────────────────────────────────────────────────────

	printBanner(cfg)

	if err := monitor.Run(ctx); err != nil {
		logger.Error("monitor encountered a fatal error", slog.String("err", err.Error()))
		monitor.PrintSummary()
		os.Exit(1)
	}

	monitor.PrintSummary()
}
