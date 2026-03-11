<div align="center">

<img src="https://raw.githubusercontent.com/lucide-icons/lucide/main/icons/lamp.svg" width="80" height="80" />

# 🔦 Lantern

**Portable LAN file sharing hub. No internet. No accounts. No friction.**

Share files between any device on your network through a browser — and carry the whole thing in your pocket on a Raspberry Pi.

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Status](https://img.shields.io/badge/Status-Phase%201a-orange?style=flat-square)]()
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey?style=flat-square)]()

[Features](#features) · [How It Works](#how-it-works) · [Getting Started](#getting-started) · [Protocol](#protocol-design) · [Roadmap](#roadmap)

---

</div>

## The Problem

AirDrop is Apple-only. Bluetooth is slow. You shouldn't have to email yourself a file or plug in a USB stick just to move something between your phone and laptop. And when you need to share files, chat, or collaborate with multiple people in the same room, you're forced to rely on cloud services that need internet.

**Lantern** is a self-hosted hub that runs on your local network. Any device on the same WiFi opens a browser and can share files, chat with others, and receive notifications. No app installs. No cloud. No accounts. Just works.

And when you put it on a Raspberry Pi Zero 2W, the entire hub fits in your pocket.

---

## Features

### File Sharing (Available Now)
- **Browser-first** — any device with a browser can upload and download, no app install needed
- **Custom binary protocol** — purpose-built TCP protocol with chunked streaming, per-chunk CRC-32 integrity, and full-file SHA-256 verification
- **Resumable transfers** — WiFi drop mid-transfer? Reconnect and continue from where you left off
- **Multi-file sessions** — drag 10 photos, one connection handles all of them
- **Hybrid storage** — files live on the server temporarily, auto-cleaned by TTL or download count
- **Bounded concurrency** — configurable upload/download limits so the Pi doesn't get overwhelmed

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
│              Lantern Server (your PC / Pi)           │
│                                                      │
│   TCP :9723  ────────────────  Raw protocol          │
│   HTTP :9724 ────────────────  Browser UI            │
│   WebSocket  ────────────────  Live progress         │
└─────────────────────────────────────────────────────┘
         ▲                  ▲
         │                  │
   CLI client          Any browser
  (Go binary)       (phone, laptop...)
```

**Upload flow:** Client connects → handshake → announces file metadata → streams chunks → server verifies integrity → file stored with TTL → download link ready

**Download flow:** Browser requests file by ID → server streams chunks → client verifies → server tracks download count → auto-deletes when exhausted

---

## Getting Started

### Prerequisites

- Go 1.21+
- A local network (or just localhost for testing)

### Build

```bash
git clone https://github.com/yourname/lantern.git
cd lantern

# Build server
go build -o bin/lantern-server ./cmd/server

# Build CLI client
go build -o bin/lantern-client ./cmd/client
```

### Run the Server

```bash
./bin/lantern-server serve

# With custom ports
./bin/lantern-server serve --tcp-port 9723 --http-port 9724

# TCP only (no browser UI)
./bin/lantern-server serve --http-port 0
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
./bin/lantern-client send ./photo.jpg

# Send multiple files
./bin/lantern-client send ./file1.pdf ./file2.zip ./file3.png

# Send with custom TTL and download limit
./bin/lantern-client send ./video.mp4 --ttl 3600 --max-downloads 3
```

### Receive a File (CLI)

```bash
./bin/lantern-client get <file-id> --output ./downloads/
```

---

## Protocol Design

Lantern uses a custom binary protocol over TCP — not HTTP, not WebRTC. Here's why and how.

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
| `HANDSHAKE` | `0x01` | Client → Server | Initiate or resume session |
| `FILE_HEADER` | `0x02` | Bidirectional | Announce file metadata |
| `CHUNK` | `0x03` | Bidirectional | One piece of a file |
| `CONTROL` | `0x04` | Bidirectional | ACK, NAK, ERROR, STATUS |

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
│   ├── server/main.go          ← Server entry point
│   └── client/main.go          ← CLI client
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
│   │   ├── session.go          ← Session state machine
│   │   ├── store.go            ← Session registry (map + mutex)
│   │   ├── transfer.go         ← Upload/download logic
│   │   ├── storage.go          ← File storage, disk checks
│   │   ├── cleanup.go          ← TTL + download-count reaper
│   │   └── semaphore.go        ← Bounded concurrency
│   │
│   ├── client/
│   │   ├── client.go           ← TCP connect, session management
│   │   ├── sender.go           ← Chunk and send files
│   │   └── receiver.go         ← Receive and reassemble
│   │
│   ├── web/                    ← Phase 1b
│   │   ├── bridge.go           ← HTTP ↔ internal adapter
│   │   ├── handler.go          ← HTTP routes + WebSocket
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
  --tcp-port 9723 \          # Raw TCP port (default: 9723)
  --http-port 9724 \         # HTTP/WebSocket port (default: 9724, 0 = disabled)
  --max-uploads 5 \          # Max concurrent uploads
  --max-downloads 10 \       # Max concurrent downloads
  --ttl 1800 \               # Default file TTL in seconds (default: 30 min)
  --max-downloads-per-file 1 \ # Default download limit per file
  --chunk-size 262144 \      # Chunk size in bytes (default: 256KB)
  --storage ./lantern-files  # Storage directory
```

---

## Roadmap

### ✅ Phase 1a — TCP Core
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
- [ ] QR code on server startup
- [x] Single binary with embedded static assets

### 📡 Phase 2 — Raspberry Pi Deployment
- [ ] mDNS discovery (`_lantern._tcp` / `lantern.local`)
- [ ] Persistent stored-file reload after restart
- [ ] Cross-restart upload resume within a bounded TTL window
- [ ] Auto-start on boot (`systemd`)
- [ ] Chunk size observability and tuning on Pi hardware
- [ ] ARM binary cross-compilation

Planning docs:

- [Phase 2 implementation plan](./PHASE2_PLAN.md)
- [Raspberry Pi deployment guide](./PI_DEPLOYMENT.md)

### 🔒 Phase 3 — Real-Time Communication
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

### 🎯 Phase 4 — Advanced Features
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
