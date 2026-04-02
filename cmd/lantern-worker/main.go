package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Harish-hex/Lantern/internal/worker"
	"github.com/Harish-hex/Lantern/internal/worker/toolchain"
)

const version = "0.1.0"

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Printf("lantern-worker v%s\n", version)
			return
		case "help", "--help", "-h":
			printUsage()
			return
		case "worker":
			args = args[1:]
		}
	}
	cmdWorker(args)
}

func cmdWorker(args []string) {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "server host")
	port := fs.Int("port", 9723, "server port")
	httpPort := fs.Int("http-port", 9724, "server HTTP port (for file downloads)")
	workerID := fs.String("worker-id", "", "worker id")
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

	tcm := toolchain.NewManager(*toolsDir, nil)
	if *autoDownload {
		tcm.AutoDownload = true
		log.Printf("[worker] toolchain auto-download enabled -- tools dir: %s", tcm.ToolsDir)
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

	runnerCfg.Registry.Register(worker.NewDataProcessingExecutor())
	runnerCfg.Registry.Register(worker.NewImageBatchExecutor())
	runnerCfg.Registry.Register(worker.NewDocumentOCRExecutor(tcm))

	r := worker.NewRunner(runnerCfg)
	if err := r.Run(context.Background()); err != nil {
		log.Fatalf("runner failed: %v", err)
	}
}

func printUsage() {
	fmt.Println(`Lantern Worker

USAGE
  lantern-worker [flags]
  lantern-worker worker [flags]  # compatibility mode

FLAGS
  --host <host>             Coordinator host (default: 127.0.0.1)
  --port <port>             Coordinator TCP port (default: 9723)
  --enroll-code <code>      Enrollment code generated in dashboard
  --device-name <name>      Friendly name shown in dashboard
  --token <token>           Compute token (optional if using enrollment)

EXAMPLE
  lantern-worker --host 192.168.1.10 --port 9723 --enroll-code ABCD1234 --device-name "Office-PC"`)
}
