<div align="center">

<img src="https://raw.githubusercontent.com/lucide-icons/lucide/main/icons/lamp.svg" width="80" height="80" />

# 🔦 Lantern

**Portable LAN compute mesh with built-in file transfer. No internet. No accounts. No friction.**

Turn spare machines on your local network into a lightweight pool for coordination, task dispatch, and data movement you control.

[![Go Version](https://img.shields.io/badge/Go-1.26.1+-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Status](https://img.shields.io/badge/Status-Compute%20Mesh%20In%20Progress-orange?style=flat-square)]()
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey?style=flat-square)]()

[Features](#features) · [How It Works](#how-it-works) · [Getting Started](#getting-started) · [Protocol](#protocol-design) · [Roadmap](#roadmap)

---

</div>

## The Problem

Most personal and small-team compute is stranded. Your laptop, desktop, Raspberry Pi, and home server all have cycles to spare, but stitching them together usually means cloud infrastructure, brittle orchestration, or heavyweight distributed systems that are overkill for a local network.

**Lantern** is a self-hosted LAN node that aims to make local computation sharing simple first: discover peers, register workers, lease work, recover from disconnects, and move the required payloads around the network. File sharing is still an important part of the story, but it supports the broader goal of turning nearby devices into a practical compute pool.

And when you put it on a Raspberry Pi Zero 2W, the coordinator can travel with you.

---

## Features

### Compute Coordination (In Progress, Already in the Codebase)
- **Worker registration and heartbeats** — LAN workers can register capabilities and maintain leases with the coordinator
- **Task leasing model** — work can be assigned to workers with lease expiry and retry-aware recovery semantics
- **Persistent coordinator state** — jobs, tasks, and worker snapshots are stored so the system can recover state after restart
- **Failure handling** — stale workers are reaped and leased tasks can be returned to the queue
- **Operator controls** — compute behavior is configurable from the CLI with toggles for token requirements, lease TTLs, heartbeat intervals, retries, and task sizing
- **Built for local-first experimentation** — lightweight enough to run on a laptop or Raspberry Pi without external services

### File Transfer (Available Now)
- **Browser-first** — any device with a browser can upload and download, no app install needed
- **Custom binary protocol** — purpose-built TCP protocol with chunked streaming, per-chunk CRC-32 integrity, and full-file SHA-256 verification
- **Resumable transfers** — WiFi drop mid-transfer? Reconnect and continue from where you left off
- **Multi-file sessions** — drag 10 files, one connection handles all of them
- **Hybrid storage** — files live on the server temporarily, auto-cleaned by TTL or download count
- **Bounded concurrency** — configurable upload/download limits so the coordinator does not get overwhelmed

### Communication & Collaboration (Coming Soon)
- **LAN chat** — real-time messaging between all connected devices on the network
- **Push notifications** — server can broadcast alerts to all clients (new files, announcements, etc.)
- **Live presence** — see who's currently connected to the hub
- **Event broadcasting** — trigger actions across multiple devices simultaneously

### Infrastructure
- **Dual protocol surface** — raw TCP for CLI and native clients, HTTP/WebSocket for browsers
- **QR code discovery** — scan to connect, no typing IPs
- **Portable** — designed to run on a Raspberry Pi Zero 2W broadcasting its own WiFi hotspot
- **Cross-platform** — works on phones, tablets, laptops — anything with a browser

---

## How It Works

```
┌─────────────────────────────────────────────────────┐
│            Lantern Coordinator (your PC / Pi)        │
│                                                      │
│   TCP :9723  ────────────────  Worker + transfer RPC │
│   HTTP :9724 ────────────────  Dashboard / browser   │
│   WebSocket  ────────────────  Live upload progress  │
└─────────────────────────────────────────────────────┘
         ▲                  ▲                    ▲
         │                  │                    │
   Worker / CLI        Browser client       Other LAN nodes
    (Go binary)        (ops + transfer)     (future compute)
```

**Compute flow today:** Worker connects → authenticates if required → registers capabilities → sends heartbeats → coordinator tracks leases and reaps stale workers → persisted state survives restart

**Transfer flow today:** Client connects → handshake → announces file metadata → streams chunks → coordinator verifies integrity → file stored with TTL/download policy → browser or CLI can retrieve it later

---

## Getting Started

### Prerequisites

- Go 1.26.1+
- A local network (or just localhost for testing)

### Build

```bash
git clone https://github.com/Harish-hex/Lantern.git
cd lantern

# Build the single Lantern binary
go build -o bin/lantern ./cmd/lantern
```

### Run the Server

```bash
./bin/lantern serve

# With custom ports
./bin/lantern serve --port 9723 --http-port 9724

# TCP only (no browser UI)
./bin/lantern serve --http-port -1
```

### Build Windows Worker `.exe` (for Dashboard Install)

```bash
# Build a Windows worker executable used by dashboard installer packaging
scripts/build_worker_windows.sh
```

This generates:

```text
bin/lantern-worker-windows-amd64.exe
```

In the dashboard:

1. Open the `Workers` tab.
2. Click `Generate Pairing Code`.
3. Click `Download Worker ZIP` (Windows package).
4. On the target Windows machine, extract the zip and run `START_WORKER.cmd`.
5. Enter a device name when prompted; the worker auto-registers using the enrollment code.

### OCR Batch Setup (Tesseract)

The `Document OCR Batch` compute template requires `tesseract` on worker machines.

From the dashboard:

1. Open the `Workers` tab.
2. In `OCR Tool Setup`, click `Check OCR Tool`.
3. Download the platform install script (Windows/Linux/macOS) and run it on each worker.
4. Start or restart the worker and confirm OCR tool status is ready.

You can also install manually:

```bash
# macOS
brew install tesseract

# Ubuntu/Debian
sudo apt-get update && sudo apt-get install -y tesseract-ocr
```

### Video Render Setup (Blender)

The `Render Frames` compute template requires `blender` on worker machines.

From the dashboard:

1. Open the `Workers` tab.
2. In `Render Tool Setup`, click `Check Render Tool`.
3. Download the platform install script (Windows/Linux/macOS) and run it on each render worker.
4. Start or restart the worker and confirm render tool status is ready.

You can also install manually:

```bash
# macOS
brew install --cask blender

# Ubuntu/Debian
sudo apt-get update && sudo apt-get install -y blender
```

On startup, you'll see:

```
┌─────────────────────────────────────┐
│         lantern is running          │
│                                     │
│   Scan to connect:                  │
│   ▄▄▄▄▄▄▄ ▄  ▄ ▄▄▄▄▄▄▄            │
│   █ ▄▄▄ █ ▀▄▀  █ ▄▄▄ █            │
│   █ ███ █ ▄▀▄  █ ███ █            │
│   ▀▀▀▀▀▀▀ ▀ ▀  ▀▀▀▀▀▀▀            │
│                                     │
│   Or visit:  lantern.local          │
│   Or type:   192.168.1.42:9724      │
└─────────────────────────────────────┘
```

### Send a File (CLI)

```bash
# Send a single file
./bin/lantern send ./photo.jpg

# Send multiple files
./bin/lantern send ./file1.pdf ./file2.zip ./file3.png

# Send with custom TTL and download limit
./bin/lantern send --ttl 3600 --max-downloads 3 ./video.mp4
```

### Receive a File (CLI)

```bash
./bin/lantern get <file-id> --dest ./downloads/
```

---

## Protocol Design

Lantern uses a custom binary protocol over TCP for both transfer and coordinator control traffic. The same transport gives the project a foundation for resumable file movement and lightweight compute orchestration without depending on external infrastructure.

### Packet Structure

```
┌──────────────────────────────────────────────┐
│  MAGIC        4 bytes   "LTRN"               │  ← Identifies Lantern traffic
│  VERSION      1 byte    0x01                 │  ← Protocol version
│  MSG_TYPE     1 byte    HANDSHAKE/FILE/CHUNK  │
│  FLAGS        1 byte    See below            │
│  RESERVED     1 byte    Future auth          │
│  PAYLOAD_LEN  4 bytes   uint32, big-endian   │
│  SEQUENCE     4 bytes   Per-file chunk index │
│  SESSION_ID   16 bytes  UUID                 │
├──────────────────────────────────────────────┤
│  PAYLOAD      variable                       │
└──────────────────────────────────────────────┘
│  CHECKSUM     4 bytes   CRC-32               │
└──────────────────────────────────────────────┘
```

**32-byte fixed header + variable payload + 4-byte checksum tail**

### Message Types

| Type | Code | Direction | Purpose |
|---|---|---|---|
| `HANDSHAKE` | `0x01` | Client → Server | Initiate or resume a session |
| `FILE_HEADER` | `0x02` | Bidirectional | Announce file metadata |
| `CHUNK` | `0x03` | Bidirectional | One piece of a file payload |
| `CONTROL` | `0x04` | Bidirectional | ACK, NAK, ERROR, STATUS, and compute control messages |

### FLAGS Byte

| Bit | Name | Meaning |
|---|---|---|
| 0 | `LAST_CHUNK` | Final chunk of a file |
| 1 | `COMPRESSED` | Payload is compressed |
| 2 | `RETRANSMIT` | Resent chunk |
| 3 | `ENCRYPTED` | Reserved for Phase 3 |
| 4–7 | — | Reserved, must be zero |

### Integrity Strategy

- **Per-chunk:** CRC-32 on every CHUNK packet — failed chunks trigger retransmission (max 3 retries)
- **Per-file:** SHA-256 declared in FILE_HEADER, verified after all chunks reassembled
- **Unique chunk key:** `SESSION_ID + FILE_INDEX + SEQUENCE`

---

## Project Structure

```
lantern/
├── cmd/
│   └── lantern/main.go         ← Single CLI entry point (`serve`, `send`, `get`)
│
├── internal/
│   ├── protocol/
│   │   ├── header.go           ← Packet struct, Marshal/Unmarshal
│   │   ├── messages.go         ← Message type definitions
│   │   ├── reader.go           ← Read packets from net.Conn
│   │   ├── writer.go           ← Write packets to net.Conn
│   │   └── checksum.go         ← CRC-32 + SHA-256
│   │
│   ├── server/
│   │   ├── server.go           ← TCP listener, accept loop
│   │   ├── handler.go          ← Protocol message handling
│   │   ├── compute.go          ← Compute coordinator state and worker leasing
│   │   ├── persist.go          ← File/session persistence + recovery
│   │   ├── storage.go          ← File storage, disk checks
│   │   ├── cleanup.go          ← TTL + download-count reaper
│   │   └── stats.go            ← Runtime transfer stats
│   │
│   ├── client/
│   │   └── client.go           ← TCP connect, upload, and download logic
│   │
│   ├── web/
│   │   ├── server.go           ← HTTP routes, SSE, and embedded static app
│   │   ├── ws_upload.go        ← WebSocket upload path
│   │   └── static/index.html   ← Browser UI (embedded)
│   │
│   └── config/config.go        ← Server configuration
│
├── go.mod
└── README.md
```

---

## Configuration

```bash
lantern serve \
  --port 9723 \                   # Raw TCP port (default: 9723)
  --http-port 9724 \              # HTTP/WebSocket port (default: 9724, -1 = disabled)
  --max-concurrent 5 \            # Max concurrent uploads
  --max-download-concurrent 10 \  # Max concurrent downloads
  --ttl-default 1800 \            # Default file TTL in seconds (default: 1 hour)
  --default-max-downloads 1 \     # Default download limit per file
  --chunk-size-kb 256 \           # Chunk size in KiB (default: 256)
  --storage ./lantern-files  # Storage directory
```

---

## Roadmap

### 🔄 Phase 3 — Compute Mesh
- [x] Worker registration
- [x] Worker heartbeats and lease tracking
- [x] Stale worker reaping
- [x] Persistent compute worker/task snapshots
- [x] CLI flags for compute coordinator controls
- [ ] Task creation and dispatch APIs
- [ ] End-to-end job lifecycle orchestration
- [ ] Worker execution protocol beyond registration/heartbeat
- [ ] Dashboard visibility into jobs, tasks, and workers
- [ ] Capability-aware scheduling
- [ ] Result collection and artifact management

### ✅ Phase 1a — Transfer Core
- [x] Custom binary protocol design
- [x] TCP server with bounded concurrency
- [x] Chunked file transfer with integrity verification
- [x] Resumable transfers (in-session)
- [x] Multi-file sessions
- [x] CLI client

### 🔄 Phase 1b — Browser UI
- [x] HTTP server on separate port
- [x] Drag-and-drop upload panel
- [x] File browser with TTL and download count
- [x] Live event stream for browser updates
- [x] Single binary with embedded static assets

### 📡 Phase 2 — Raspberry Pi + LAN Deployment
- [x] mDNS discovery (`_lantern._tcp` / `lantern.local`)
- [x] Persistent stored-file reload after restart
- [x] Cross-restart upload resume within a bounded TTL window
- [x] Auto-start on boot (`systemd`)
- [x] Chunk size observability and tuning on Pi hardware
- [x] ARM binary cross-compilation

### 📱 Phase 2.5 — Mobile Onboarding + Dashboard Transfer Parity
- [x] Terminal QR output on startup for dashboard connection
- [x] Print both raw-IP and mDNS dashboard URLs at startup
- [x] Dashboard upload path moved to WebSocket chunked transfer
- [x] ACK/NAK + bounded retry behavior for WebSocket chunk uploads
- [x] SSE retained for lightweight live event updates

Planning docs:

- [Phase 2 implementation plan](./PHASE2_PLAN.md)

Automation helpers:

- `scripts/install_auto_verify_push_hook.sh` installs a local `post-commit` hook
- `scripts/auto_verify_push.sh` runs `go test ./...` in the background and pushes the current branch only if verification passes and the worktree stayed clean

### 🔒 Phase 4 — Real-Time Communication
- [ ] **LAN chat** — group and direct messaging between connected devices
- [ ] **Push notifications** — server-to-client event broadcasting
- [ ] **Typing indicators** — real-time user activity signals
- [ ] **Presence system** — see who's online/offline
- [ ] **Message persistence** — chat history stored on server
- [ ] **Event hooks** — trigger client actions from server (refresh UI, alerts, etc.)
- [ ] **Broadcast channels** — pub/sub messaging for different groups
- [ ] Token-based authentication
- [ ] TLS encryption
- [ ] Captive portal DNS (hotspot mode)

### 🎯 Phase 5 — Advanced Features
- [ ] **Screen sharing** — stream viewport over WebRTC with socket signaling
- [ ] **Collaborative whiteboards** — real-time drawing with conflict resolution
- [ ] **Live polls/quizzes** — interactive Q&A sessions
- [ ] **File change notifications** — alert clients when new files are uploaded
- [ ] Persistent storage mode (NAS-like)
- [ ] Multi-room support (separate file/chat spaces)

---

## Hardware (Portable Hub)

| Component | Model | Cost |
|---|---|---|
| Compute | Raspberry Pi Zero 2W | ~$15 |
| Storage | 32GB SD Card | ~$8 |
| Power | USB Power Bank | ~$20 |
| Cables | USB OTG adapter | ~$5 |
| **Total** | | **~$48** |

Expected transfer speeds in Pi hotspot mode: **2–8 MB/s** (single client). Fast enough for photos, documents, and short clips. Not a video streaming server.

---

## Why Not Just Use...

| Alternative | Problem |
|---|---|
| AirDrop | Apple devices only, no chat/notifications |
| Bluetooth | Slow, pairing friction, point-to-point only |
| Google Drive / iCloud | Requires internet, account, app |
| USB | Physical cable required |
| Local HTTP server | No custom protocol, no resume, no sessions, no real-time features |
| Slack/Discord | Requires internet and accounts |
| **Lantern** | Works on any device, any OS, no internet, no accounts, real-time comms built-in |

---

## Contributing

This is a learning project built to explore Go, custom protocol design, and embedded Linux. Issues, ideas, and PRs are welcome.

```bash
# Run tests
go test ./...

# Lint
go vet ./...
```

---

## License

MIT — do whatever you want with it.

---

<div align="center">

Built with Go · Designed for the Pi · Works everywhere

</div>
