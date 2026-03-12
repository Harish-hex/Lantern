# Lantern — Design Document

> Portable LAN file sharing hub. Go server + browser UI. Eventually runs on a Raspberry Pi Zero 2W.

---

## Phase Plan

| Phase | Scope | Deliverable |
|---|---|---|
| **1a** | Raw TCP server + custom protocol | Working file transfer via CLI client |
| **1b** | HTTP/WebSocket layer on top | Browser UI for upload/download |
| **2** | mDNS discovery, Pi deployment, cross-restart resume, chunk size tuning | Portable hub on Pi Zero 2W |
| **2.5** | Mobile onboarding QR + dashboard WebSocket chunk transfer parity | Fast phone connect + resilient browser uploads |
| **3** | Chat, notifications, encryption, hotspot management | Full-featured LAN tool |

---

## Architecture

**Model:** Hybrid — files temporarily stored on server, auto-cleaned.

| Decision | Choice |
|---|---|
| Cleanup | TTL + download-count (whichever triggers first) |
| Communication | Dual-protocol: raw TCP (custom) + HTTP/WebSocket |
| File handling | Chunked streaming (256KB), resumable, multi-file sessions |
| Size validation | Two-level: protocol header check + dynamic disk space check |
| Discovery | QR code + raw IP (Phase 2.5). mDNS in Phase 2 |
| Concurrency | Bounded: configurable N uploads + M downloads |
| Security | Fully open Phase 1. Auth field reserved in protocol header |
| Resume | In-session only (WiFi drops). Cross-restart deferred to Phase 2 |

---

## Protocol Specification

### Packet Structure (32-byte fixed header)

```
┌──────────────────────────────────────────────┐
│  MAGIC (4 bytes)     "LTRN"                  │  Identifies Lantern traffic
│  VERSION (1 byte)     0x01                   │  Protocol version
│  MSG_TYPE (1 byte)                           │  HANDSHAKE/FILE_HEADER/CHUNK/CONTROL
│  FLAGS (1 byte)                              │  See FLAGS definition below
│  RESERVED (1 byte)                           │  Future auth token ref
│  PAYLOAD_LEN (4 bytes, uint32, big-endian)   │  Per-packet limit (~4GB), NOT per-file
│  SEQUENCE (4 bytes)                          │  Per-file chunk index, resets per file
│  SESSION_ID (16 bytes, UUID)                 │  Ties multi-file transfers
├──────────────────────────────────────────────┤
│  PAYLOAD (variable length)                   │
└──────────────────────────────────────────────┘
│  CHECKSUM (4 bytes, CRC-32)                  │  Per-chunk integrity
└──────────────────────────────────────────────┘
```

**Unique key per chunk:** `SESSION_ID + FILE_INDEX + SEQUENCE`

### Message Types

| Type | Code | Direction | Purpose |
|---|---|---|---|
| `HANDSHAKE` | `0x01` | Client → Server | Initiate or resume session |
| `FILE_HEADER` | `0x02` | Client → Server (upload) / Server → Client (download) | Announce file metadata |
| `CHUNK` | `0x03` | Bidirectional | One piece of a file |
| `CONTROL` | `0x04` | Bidirectional | ACK, NAK, ERROR, STATUS, BUSY, RESUME |

### FLAGS Byte

| Bit | Name | Meaning |
|---|---|---|
| 0 | `LAST_CHUNK` | Final chunk of a file |
| 1 | `COMPRESSED` | Payload is compressed |
| 2 | `RETRANSMIT` | Resent chunk |
| 3 | `ENCRYPTED` | Reserved for Phase 3 |
| 4–7 | — | Must be zero (reserved for future use) |

### FILE_HEADER Payload (JSON)

```json
{
  "filename": "video.mp4",
  "size": 2147483648,
  "mime_type": "video/mp4",
  "checksum_full": "sha256:ab3f...",
  "chunk_size": 262144,
  "total_chunks": 8192,
  "file_index": 1,
  "total_files": 3,
  "max_downloads": 1,
  "ttl_seconds": 1800
}
```

Server enforces its own configured maximums — client values are capped, not trusted.

---

## Transfer Flows

### Upload (Client → Server)

```
CLIENT                              SERVER
  │                                    │
  ├─── HANDSHAKE ─────────────────────►│  Session begins
  │◄── CONTROL (ACK, session_id) ──────┤  Server assigns SESSION_ID
  │                                    │
  ├─── FILE_HEADER ───────────────────►│  Server validates:
  │                                    │   1. File size vs available disk space
  │                                    │   2. Concurrent upload limit (semaphore)
  │                                    │   3. Size vs server maximum
  │◄── CONTROL (ACK or REJECT) ───────┤
  │                                    │
  ├─── CHUNK (seq=0) ────────────────►│  Server: verify CRC, write to temp
  │◄── CONTROL (ACK seq=0) ───────────┤
  │         ...                        │
  ├─── CHUNK (seq=N, LAST_CHUNK) ────►│  Server: verify full SHA-256
  │◄── CONTROL (COMPLETE, file_id) ───┤  Move to storage, start TTL timer
```

### Download (Server → Client)

```
CLIENT                              SERVER
  │                                    │
  ├─── HANDSHAKE ─────────────────────►│
  │◄── CONTROL (ACK, session_id) ──────┤
  │                                    │
  ├─── CONTROL (DOWNLOAD, file_id) ──►│  Server validates:
  │                                    │   1. File exists
  │                                    │   2. Download count not exhausted
  │                                    │   3. TTL not expired
  │                                    │   4. Concurrent download limit
  │                                    │
  │◄── FILE_HEADER ────────────────────┤  Roles reversed
  │◄── CHUNK (seq=0) ─────────────────┤  Client verifies CRC per chunk
  ├─── CONTROL (ACK seq=0) ──────────►│
  │         ...                        │
  │◄── CHUNK (seq=N, LAST_CHUNK) ─────┤  Client verifies full SHA-256
  ├─── CONTROL (COMPLETE) ───────────►│  Server increments download_count
  │                                    │  If count >= max → delete file
```

**Download count rule:** Never interrupt an in-progress download because count hits zero. Decrement-and-check happens at COMPLETE. If two clients race, both finish — file deletes after the last one completes.

---

## Error Handling

### Failure Catalog

| Failure | Server Response | Client Behavior |
|---|---|---|
| **Chunk CRC mismatch** | `CONTROL(NAK, seq, "CRC_MISMATCH")` | Retransmit chunk. Max 3 retries → abort |
| **Connection drop** | Mark session PAUSED, hold temp data 5 min | Reconnect with same SESSION_ID. Server sends `CONTROL(RESUME, last_acked_seq)` |
| **Disk full mid-transfer** | `CONTROL(ERROR, "DISK_FULL")`, cleanup temp | Client gets clean error |
| **Chunk count mismatch** | `CONTROL(ERROR, "CHUNK_COUNT_MISMATCH")` | Abort |
| **Full-file SHA-256 mismatch** | `CONTROL(ERROR, "INTEGRITY_FAILED")`, delete file | Must restart entire file |
| **Server at capacity** | `CONTROL(BUSY, retry_after=5)` | Max 5 retries, exponential backoff (5s→80s), then error |
| **File expired/exhausted** | `CONTROL(ERROR, "FILE_EXPIRED")` | Surface error to user |
| **Multi-file partial failure** | `CONTROL(PARTIAL_COMPLETE, succeeded=[], failed=[])` | Files that succeeded remain downloadable |

### Edge Case Rules

| Edge Case | Rule |
|---|---|
| Duplicate chunk (already ACK'd seq) | Silently discard, regardless of RETRANSMIT flag |
| Resume with mismatched metadata | Validate FILE_HEADER against paused session. Mismatch → `SESSION_CONFLICT` → new session required |
| Integrity fail after resume | No automatic retry. Session → `PARTIAL_COMPLETE`. Client decides |
| Concurrent download race | Atomic counter. Both finish. File deletes after last completes |

---

## Project Structure

```
lantern/
├── cmd/
│   ├── server/
│   │   └── main.go              ← Server entry point
│   └── client/
│       └── main.go              ← CLI client (Phase 1a testing)
│
├── internal/
│   ├── protocol/
│   │   ├── header.go            ← Packet struct, Marshal/Unmarshal
│   │   ├── messages.go          ← Message type definitions
│   │   ├── reader.go            ← Read packets from net.Conn
│   │   ├── writer.go            ← Write packets to net.Conn
│   │   └── checksum.go          ← CRC-32 per chunk, SHA-256 per file
│   │
│   ├── server/
│   │   ├── server.go            ← TCP listener, accept loop, dispatch
│   │   ├── session.go           ← Session state machine logic
│   │   ├── store.go             ← Session registry (map + mutex)
│   │   ├── transfer.go          ← Upload/download handling
│   │   ├── storage.go           ← File storage, temp files, disk checks
│   │   ├── cleanup.go           ← TTL + download-count reaper goroutine
│   │   └── semaphore.go         ← Bounded concurrency limiter
│   │
│   ├── client/
│   │   ├── client.go            ← TCP connect, session management
│   │   ├── sender.go            ← Chunk and send files
│   │   └── receiver.go          ← Receive chunks and reassemble
│   │
│   └── config/
│       └── config.go            ← Server config (ports, limits, defaults)
│
├── web/                          ← Phase 1b (empty until then)
│   ├── handler.go               ← HTTP handlers, WebSocket upgrade
│   ├── static/                   ← HTML/CSS/JS for browser UI
│   └── bridge.go                ← HTTP ↔ TCP protocol bridge
│                                  ⚠ Split if >200 lines
│
├── go.mod
├── go.sum
└── README.md
```

**Key constraints:**
- `store.go` is the **single canonical owner** of session state in memory. Protected by mutex. All goroutines go through this.
- `protocol/` is imported by both server and client — guarantees they speak the same language.
- `internal/` prevents external imports. Promote to `pkg/` only if you explicitly want a public Go API.

---

## Explicit Non-Goals (Phase 1)

- No user accounts or authentication enforcement
- No mDNS / Bonjour discovery
- No chat, notifications, or real-time collaboration
- No WiFi hotspot management
- No encryption or TLS
- No mobile-native apps (browser only)
- No cross-restart resume
- No chunk size optimization (start at 256KB, benchmark on Pi in Phase 2)

---

## Phase 2 Implementation Summary

Phase 2 builds on the current architecture rather than replacing it.

### Current starting point

- In-memory session pause/resume already exists in `internal/server/session.go`
- `StorageManager` already backs both the TCP and HTTP paths
- chunk size and concurrency are already configurable in `internal/config/config.go`
- restart resilience and service discovery do not exist yet

### Planned additions

#### 1. mDNS discovery

- Add `internal/discovery/mdns.go`
- Extend `config.Config` with `EnableMDNS` and `MDNSServiceName`
- Wire advertiser startup and shutdown from `cmd/lantern/main.go`
- Advertise the raw TCP service and expose the HTTP dashboard port via TXT records or a second service

#### 2. Pi Zero 2W deployment profile

- Keep `config.Config` as the source of truth
- Document a conservative operating profile for concurrency, chunk size, TTL, and file-size limits
- Add a dedicated deployment guide with ARM build instructions and a `systemd` unit

#### 3. Cross-restart resume

- Add `internal/index/index.go` for persisted session and stored-file snapshots
- Restore completed stored files into `StorageManager` on startup
- Restore paused upload sessions whose temp files still exist and whose resume window has not expired
- Keep the current wire protocol initially and reuse `SESSION_ID` semantics where possible

#### 4. Chunk-size tuning

- Add transfer stats collection in the server path
- Expose stats through an HTTP endpoint such as `/api/stats`
- Surface chunk-size configuration more clearly in the CLI and Pi docs
- Focus on manual tuning first, not automatic adaptation

### Rollout order

1. persistent stored-file reload
2. mDNS advertiser
3. Pi deployment documentation and config surfacing
4. persisted paused-session restore
5. transfer stats and chunk-size benchmarking support

### Supporting docs

- Detailed implementation plan: [PHASE2_PLAN.md](/Users/harishharish/Projects/Lantern/Lantern/PHASE2_PLAN.md)
- Raspberry Pi operations guide: [PI_DEPLOYMENT.md](/Users/harishharish/Projects/Lantern/Lantern/PI_DEPLOYMENT.md)
