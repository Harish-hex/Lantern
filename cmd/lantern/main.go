// Lantern — a lightweight, resumable file transfer tool over TCP.
//
// Usage:
//
//	lantern serve                 Start the Lantern server
//	lantern send <files...>       Upload files to a Lantern server
//	lantern get  <fileID>         Download a file by its ID
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Harish-hex/Lantern/internal/client"
	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/discovery"
	"github.com/Harish-hex/Lantern/internal/protocol"
	"github.com/Harish-hex/Lantern/internal/server"
	"github.com/Harish-hex/Lantern/internal/web"
	"github.com/Harish-hex/Lantern/internal/worker"
	"github.com/Harish-hex/Lantern/internal/worker/toolchain"
	qrterminal "github.com/mdp/qrterminal/v3"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "send":
		cmdSend(os.Args[2:])
	case "get":
		cmdGet(os.Args[2:])
	case "compute-submit":
		cmdComputeSubmit(os.Args[2:])
	case "compute-status":
		cmdComputeStatus(os.Args[2:])
	case "worker":
		cmdWorker(os.Args[2:])
	case "version":
		fmt.Printf("Lantern v%s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// ──────────────────── serve ────────────────────

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "0.0.0.0", "listen address")
	port := fs.Int("port", 9723, "listen port")
	storageDir := fs.String("storage", "", "storage directory (default: ~/.lantern/storage)")
	tempDir := fs.String("temp", "", "temp directory (default: ~/.lantern/temp)")
	maxConcurrent := fs.Int("max-concurrent", 8, "max concurrent uploads")
	maxDownloadConcurrent := fs.Int("max-download-concurrent", 0, "max concurrent downloads (0 = use config default)")
	httpPort := fs.Int("http-port", 0, "HTTP dashboard port (0 = use config default, -1 = disable HTTP)")
	maxFileSizeMB := fs.Int64("max-file-size-mb", 0, "max upload file size in MiB (0 = use config default)")
	chunkSizeKB := fs.Int("chunk-size-kb", 0, "chunk size in KiB (0 = use config default)")
	sessionTimeout := fs.Duration("session-timeout", 0, "active session idle timeout (0 = use config default)")
	resumeTTL := fs.Duration("resume-ttl", 0, "paused session resume window (0 = use config default)")
	ttlDefault := fs.Int("ttl-default", 0, "default file TTL in seconds (0 = use config default)")
	defaultMaxDownloads := fs.Int("default-max-downloads", 0, "default max downloads per file (0 = use config default)")
	computeEnabled := fs.Bool("compute", true, "enable compute coordinator")
	computeTokenTTL := fs.Duration("compute-token-ttl", 0, "worker token TTL (0 = use config default)")
	computeLeaseTTL := fs.Duration("compute-lease-ttl", 0, "task lease TTL (0 = use config default)")
	computeHeartbeat := fs.Duration("compute-heartbeat", 0, "worker heartbeat interval (0 = use config default)")
	computeRetryMax := fs.Int("compute-retry-max", 0, "max task retries (0 = use config default)")
	computeTaskSizeMB := fs.Int64("compute-task-size-mb", 0, "target task payload size in MiB (0 = use config default)")
	computeRequireToken := fs.Bool("compute-require-token", false, "require worker token for compute controls")
	computeWorkerToken := fs.String("compute-token", "", "shared compute worker token (optional)")
	enableMDNS := fs.Bool("mdns", false, "advertise the Lantern service over mDNS")
	mdnsName := fs.String("mdns-name", "", "mDNS service instance name (default: config default)")
	fs.Parse(args)

	cfg := config.Default()
	cfg.ListenAddr = *addr
	cfg.Port = *port
	cfg.MaxUploadConcurrency = *maxConcurrent
	if *maxDownloadConcurrent > 0 {
		cfg.MaxDownloadConcurrency = *maxDownloadConcurrent
	}
	if *maxFileSizeMB > 0 {
		cfg.MaxFileSize = *maxFileSizeMB * 1024 * 1024
	}
	if *chunkSizeKB > 0 {
		cfg.ChunkSize = uint32(*chunkSizeKB * 1024)
	}
	if *sessionTimeout > 0 {
		cfg.SessionTimeout = *sessionTimeout
	}
	if *resumeTTL > 0 {
		cfg.ResumeTTL = *resumeTTL
	}
	if *ttlDefault > 0 {
		cfg.TTLDefault = *ttlDefault
	}
	if *defaultMaxDownloads > 0 {
		cfg.MaxDownloads = *defaultMaxDownloads
	}
	cfg.ComputeEnabled = *computeEnabled
	if *computeTokenTTL > 0 {
		cfg.ComputeTokenTTL = *computeTokenTTL
	}
	if *computeLeaseTTL > 0 {
		cfg.ComputeLeaseTTL = *computeLeaseTTL
	}
	if *computeHeartbeat > 0 {
		cfg.ComputeHeartbeat = *computeHeartbeat
	}
	if *computeRetryMax > 0 {
		cfg.ComputeRetryMax = *computeRetryMax
	}
	if *computeTaskSizeMB > 0 {
		cfg.ComputeTaskSizeBytes = *computeTaskSizeMB * 1024 * 1024
	}
	if *computeRequireToken {
		cfg.ComputeRequireToken = true
	}
	if *computeWorkerToken != "" {
		cfg.ComputeWorkerAuthToken = *computeWorkerToken
		cfg.ComputeRequireToken = true
	}
	cfg.EnableMDNS = *enableMDNS
	if *mdnsName != "" {
		cfg.MDNSServiceName = *mdnsName
	}

	// HTTP configuration:
	// - If http-port is 0, keep the default from config.Default().
	// - If http-port is -1, disable HTTP entirely.
	// - Otherwise, use the provided port.
	switch {
	case *httpPort == 0:
		// leave cfg.HTTPPort as-is
	case *httpPort < 0:
		cfg.HTTPPort = 0
	default:
		cfg.HTTPPort = *httpPort
	}

	if *storageDir != "" {
		cfg.StorageDir = *storageDir
	}
	if *tempDir != "" {
		cfg.TempDir = *tempDir
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	// Optional HTTP dashboard server.
	var httpSrv *web.Server
	if cfg.HTTPPort > 0 {
		httpSrv = web.New(srv.Bridge())
		go func() {
			if err := httpSrv.Start(); err != nil {
				log.Printf("http server error: %v", err)
			}
		}()
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stopAdvertiser func()
	if cfg.EnableMDNS {
		stopFn, err := discovery.StartAdvertiser(runCtx, discovery.ServiceInfo{
			Instance: cfg.MDNSServiceName,
			TCPPort:  cfg.Port,
			HTTPPort: cfg.HTTPPort,
		})
		if err != nil {
			log.Fatalf("failed to start mDNS advertiser: %v", err)
		}
		stopAdvertiser = stopFn
		log.Printf("mDNS advertising enabled for %q", cfg.MDNSServiceName)
	}

	if cfg.HTTPPort > 0 {
		rawURL, mdnsURL := dashboardURLs(cfg)
		printDashboardConnectInfo(rawURL, mdnsURL)
	}

	// Graceful shutdown on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("\nshutting down...")
		cancel()
		if stopAdvertiser != nil {
			stopAdvertiser()
		}
		srv.Stop()
		if httpSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := httpSrv.Stop(ctx); err != nil {
				log.Printf("http shutdown error: %v", err)
			}
		}
		os.Exit(0)
	}()

	if err := srv.Start(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// ──────────────────── send ────────────────────

func cmdSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "server host")
	port := fs.Int("port", 9723, "server port")
	ttl := fs.Int("ttl", 0, "time-to-live in seconds (0 = server default)")
	maxDL := fs.Int("max-downloads", 0, "max download count (0 = server default)")
	fs.Parse(args)

	files := fs.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no files specified")
		fmt.Fprintln(os.Stderr, "usage: lantern send [flags] <file1> [file2] ...")
		os.Exit(1)
	}

	// Validate all files exist before connecting
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			log.Fatalf("file not found: %s", f)
		}
	}

	c, err := client.New(*host, *port)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	progressFn := func(filename string, pct float64) {
		bar := progressBar(pct, 30)
		fmt.Printf("\r  %-30s %s %5.1f%%", filename, bar, pct)
		if pct >= 100 {
			fmt.Println()
		}
	}

	fmt.Printf("Sending %d file(s)...\n", len(files))
	fileIDs, err := c.SendFiles(files, *ttl, *maxDL, progressFn)
	if err != nil {
		log.Fatalf("\ntransfer failed: %v", err)
	}

	fmt.Println("\n✓ Transfer complete!")
	fmt.Println("File IDs:")
	for _, id := range fileIDs {
		fmt.Printf("  → %s\n", id)
	}
}

// ──────────────────── get ────────────────────

func cmdGet(args []string) {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "server host")
	port := fs.Int("port", 9723, "server port")
	dest := fs.String("dest", ".", "destination directory")
	fs.Parse(args)

	fileID := fs.Arg(0)
	if fileID == "" {
		fmt.Fprintln(os.Stderr, "no file ID specified")
		fmt.Fprintln(os.Stderr, "usage: lantern get [flags] <fileID>")
		os.Exit(1)
	}

	c, err := client.New(*host, *port)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	progressFn := func(filename string, pct float64) {
		bar := progressBar(pct, 30)
		fmt.Printf("\r  %-30s %s %5.1f%%", filename, bar, pct)
		if pct >= 100 {
			fmt.Println()
		}
	}

	fmt.Printf("Downloading %s...\n", fileID)
	if err := c.DownloadFile(fileID, *dest, progressFn); err != nil {
		log.Fatalf("\ndownload failed: %v", err)
	}

	fmt.Println("✓ Download complete!")
}

// ──────────────────── compute-submit ────────────────────

func cmdComputeSubmit(args []string) {
	fs := flag.NewFlagSet("compute-submit", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "server host")
	port := fs.Int("port", 9723, "server port")
	jobID := fs.String("job-id", "", "optional job id")
	templateID := fs.String("template", "data_processing_batch", "built-in template id")
	jobType := fs.String("type", "", "legacy raw task type or alias for --template")
	inputsJSON := fs.String("inputs", "", "template inputs as JSON")
	settingsJSON := fs.String("settings", "", "template settings as JSON")
	rawMode := fs.Bool("raw", false, "submit raw task payloads instead of a built-in template")
	taskCount := fs.Int("tasks", 1, "number of demo tasks for raw mode")
	taskPayload := fs.String("task-payload", "", "JSON payload template for each raw-mode task")
	token := fs.String("token", "", "compute auth token (if required by server)")
	fs.Parse(args)

	c, err := client.New(*host, *port)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	if strings.TrimSpace(*jobType) != "" && strings.TrimSpace(*templateID) == "" {
		*templateID = *jobType
	}

	var resp *protocol.ControlPayload
	if *rawMode {
		if *taskCount <= 0 {
			log.Fatal("tasks must be greater than zero")
		}

		tasks := make([]json.RawMessage, 0, *taskCount)
		if strings.TrimSpace(*taskPayload) != "" {
			var raw json.RawMessage
			if err := json.Unmarshal([]byte(*taskPayload), &raw); err != nil {
				log.Fatalf("invalid task-payload JSON: %v", err)
			}
			for i := 0; i < *taskCount; i++ {
				tasks = append(tasks, append(json.RawMessage(nil), raw...))
			}
		} else {
			for i := 0; i < *taskCount; i++ {
				payload, err := json.Marshal(map[string]any{
					"task_index": i,
					"op":         "echo",
				})
				if err != nil {
					log.Fatalf("build task payload: %v", err)
				}
				tasks = append(tasks, payload)
			}
		}

		rawType := strings.TrimSpace(*jobType)
		if rawType == "" {
			rawType = "generic_v1"
		}
		resp, err = c.SubmitComputeJob(*jobID, rawType, *token, tasks)
	} else {
		var inputs json.RawMessage
		var settings json.RawMessage
		if strings.TrimSpace(*inputsJSON) != "" {
			if err := json.Unmarshal([]byte(*inputsJSON), &inputs); err != nil {
				log.Fatalf("invalid inputs JSON: %v", err)
			}
		}
		if strings.TrimSpace(*settingsJSON) != "" {
			if err := json.Unmarshal([]byte(*settingsJSON), &settings); err != nil {
				log.Fatalf("invalid settings JSON: %v", err)
			}
		}
		resp, err = c.SubmitComputeTemplateJob(*jobID, *templateID, *token, inputs, settings)
	}
	if err != nil {
		log.Fatalf("submit job: %v", err)
	}

	var accepted struct {
		Job client.ComputeJob `json:"job"`
	}
	if len(resp.Payload) > 0 {
		_ = json.Unmarshal(resp.Payload, &accepted)
	}

	fmt.Println("✓ Compute job submitted")
	fmt.Printf("Job ID: %s\n", resp.JobID)
	fmt.Printf("Status: %s\n", resp.Status)
	fmt.Printf("Info:   %s\n", resp.Message)
	if accepted.Job.TemplateName != "" {
		fmt.Printf("Template: %s\n", accepted.Job.TemplateName)
	}
	if accepted.Job.Confidence != "" {
		fmt.Printf("Confidence: %s\n", accepted.Job.Confidence)
	}
	for _, check := range accepted.Job.Preflight.Checks {
		fmt.Printf("Preflight [%s] %s\n", strings.ToUpper(check.Status), check.Message)
	}
}

// ──────────────────── compute-status ────────────────────

func cmdComputeStatus(args []string) {
	fs := flag.NewFlagSet("compute-status", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "server host")
	port := fs.Int("port", 9723, "server port")
	jobID := fs.String("job-id", "", "job id")
	watch := fs.Bool("watch", false, "poll status until terminal state")
	interval := fs.Duration("interval", 2*time.Second, "watch poll interval")
	token := fs.String("token", "", "compute auth token (if required by server)")
	fs.Parse(args)

	if *jobID == "" {
		if fs.NArg() > 0 {
			*jobID = fs.Arg(0)
		} else {
			log.Fatal("job id is required (use --job-id or positional argument)")
		}
	}

	c, err := client.New(*host, *port)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	for {
		status, err := c.ComputeJobStatus(*jobID, *token)
		if err != nil {
			log.Fatalf("query job status: %v", err)
		}

		fmt.Printf("Job %s (%s): completed=%d failed=%d total=%d\n",
			status.Job.ID,
			status.Job.Status,
			status.Job.CompletedTasks,
			status.Job.FailedTasks,
			status.Job.TotalTasks,
		)
		if status.Job.TemplateName != "" {
			fmt.Printf("Template: %s | Confidence: %s\n", status.Job.TemplateName, status.Job.Confidence)
		}
		if status.Job.NeedsAttentionReason != "" {
			fmt.Printf("Needs Attention: %s\n", status.Job.NeedsAttentionReason)
		}
		for _, task := range status.Tasks {
			if task.Status == "queued" || task.Status == "leased" || task.Status == "retrying" || task.Status == "completed" || task.Status == "needs_attention" {
				fmt.Printf("  - %s status=%s worker=%s attempt=%d\n", task.ID, task.Status, task.WorkerID, task.Attempt)
			}
		}
		for _, artifact := range status.Job.Artifacts {
			fmt.Printf("Artifact: %s (%s, %d bytes)\n", artifact.Name, artifact.Kind, artifact.SizeBytes)
		}

		if !*watch || (status.Job.Status == "done" || status.Job.Status == "needs_attention") {
			return
		}

		time.Sleep(*interval)
	}
}

// ──────────────────── worker ────────────────────

func cmdWorker(args []string) {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "server host")
	port := fs.Int("port", 9723, "server port")
	httpPort := fs.Int("http-port", 9724, "server HTTP port (for file downloads)")
	workerID := fs.String("worker-id", "", "worker id")
	capabilities := fs.String("capabilities", defaultWorkerCapabilityCSV(), "comma-separated capability list")
	token := fs.String("token", "", "compute auth token (if required by server)")
	enrollCode := fs.String("enroll-code", "", "short-lived enrollment code generated from dashboard")
	deviceName := fs.String("device-name", "", "human-friendly worker device name shown in dashboard")
	heartbeat := fs.Duration("heartbeat", 5*time.Second, "worker heartbeat interval")
	poll := fs.Duration("poll", 2*time.Second, "task claim poll interval")
	oneShot := fs.Bool("oneshot", false, "exit after one successful task")
	taskTimeout := fs.Duration("task-timeout", 5*time.Minute, "maximum time for a single task execution")
	taskZipThresholdMB := fs.Int64("task-zip-threshold-mb", 8, "zip task outputs only when total output exceeds this threshold (single-file small outputs upload directly)")
	toolsDir := fs.String("tools-dir", "", "directory for auto-downloaded tool binaries (default: ~/.lantern/tools)")
	autoDownload := fs.Bool("auto-download", false, "automatically download missing toolchain binaries")
	fs.Parse(args)

	if *workerID == "" {
		*workerID = fmt.Sprintf("worker-%d", time.Now().Unix())
	}

	// Build the toolchain manager.
	tcm := toolchain.NewManager(*toolsDir, nil)
	if *autoDownload {
		tcm.AutoDownload = true
		log.Printf("[worker] toolchain auto-download enabled — tools dir: %s", tcm.ToolsDir)
	}

	runnerCfg := worker.RunnerConfig{
		Host:                  *host,
		Port:                  *port,
		HTTPPort:              *httpPort,
		WorkerID:              *workerID,
		Token:                 *token,
		EnrollCode:            strings.TrimSpace(*enrollCode),
		DeviceName:            strings.TrimSpace(*deviceName),
		OSInfo:                runtime.GOOS,
		Heartbeat:             *heartbeat,
		PollInterval:          *poll,
		OneShot:               *oneShot,
		TaskTimeout:           *taskTimeout,
		TaskZipThresholdBytes: *taskZipThresholdMB * 1024 * 1024,
		Registry:              worker.NewRegistry(),
		ToolchainManager:      tcm,
	}

	// Register all available executors.
	runnerCfg.Registry.Register(worker.NewDataProcessingExecutor())
	runnerCfg.Registry.Register(worker.NewImageBatchExecutor())
	runnerCfg.Registry.Register(worker.NewDocumentOCRExecutor(tcm))

	// Override capabilities if requested (usually we let registry define it)
	if *capabilities != defaultWorkerCapabilityCSV() {
		log.Printf("Warning: --capabilities override is deprecated, capabilities are derived from the registry.")
	}

	r := worker.NewRunner(runnerCfg)
	if err := r.Run(context.Background()); err != nil {
		log.Fatalf("runner failed: %v", err)
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return strings.Split(defaultWorkerCapabilityCSV(), ",")
	}
	return out
}

func defaultWorkerCapabilityCSV() string {
	capabilities := make([]string, 0, len(server.BuiltInComputeTemplates())+1)
	for _, template := range server.BuiltInComputeTemplates() {
		capabilities = append(capabilities, template.ID)
	}
	capabilities = append(capabilities, "generic_v1")
	return strings.Join(capabilities, ",")
}

// ──────────────────── helpers ────────────────────

func progressBar(pct float64, width int) string {
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func printUsage() {
	fmt.Println(`Lantern — lightweight, resumable file transfers

COMMANDS
  serve           Start the Lantern server
  send <files>    Upload files to a server
  get  <fileID>   Download a file by ID
	compute-submit  Submit a compute job
	compute-status  View compute job status
	worker          Run a basic compute worker loop
  version         Print version
  help            Show this help

EXAMPLES
  lantern serve
  lantern serve -http-port 9724 -mdns
  lantern send -host 10.0.0.5 photo.jpg video.mp4
	lantern get  -host 10.0.0.5 abc123_0
	lantern compute-submit --tasks 3 --type generic_v1
	lantern worker --worker-id worker-1 --oneshot
	lantern compute-status --job-id job_123 --watch`)
}

func dashboardURLs(cfg config.Config) (rawURL, mdnsURL string) {
	ip := detectLocalIPv4()
	if ip == "" {
		ip = "127.0.0.1"
	}
	rawURL = fmt.Sprintf("http://%s:%d", ip, cfg.HTTPPort)

	host, err := os.Hostname()
	if err != nil {
		host = "lantern"
	}
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.ReplaceAll(host, " ", "-")
	host = strings.TrimSuffix(host, ".local")
	if host == "" {
		host = "lantern"
	}
	mdnsURL = fmt.Sprintf("http://%s.local:%d", host, cfg.HTTPPort)
	return rawURL, mdnsURL
}

func detectLocalIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil {
				continue
			}
			return ip.String()
		}
	}
	return ""
}

func printDashboardConnectInfo(rawURL, mdnsURL string) {
	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("Lantern Dashboard Connect")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Raw IP URL : %s\n", rawURL)
	fmt.Printf("mDNS URL   : %s\n", mdnsURL)
	fmt.Println()
	fmt.Println("Scan (Raw IP):")
	qrterminal.GenerateHalfBlock(rawURL, qrterminal.L, os.Stdout)
	fmt.Println()
	fmt.Println("Scan (mDNS):")
	qrterminal.GenerateHalfBlock(mdnsURL, qrterminal.L, os.Stdout)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
}
