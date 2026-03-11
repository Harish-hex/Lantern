# Lantern Phase 2 Plan

This document scopes Phase 2 against the current Lantern codebase and turns the high-level roadmap into an implementation order that fits the existing packages:

- TCP server: `internal/server`
- HTTP dashboard: `internal/web`
- shared config: `internal/config`
- CLI entrypoint: `cmd/lantern/main.go`

## Current Baseline

The repository already has the following building blocks:

- In-memory upload session tracking via `internal/server/session.go` and `internal/server/store.go`
- In-session resume after disconnects while the process remains alive
- Shared `StorageManager` used by both TCP and HTTP paths
- Configurable chunk size and upload/download concurrency in `internal/config/config.go`
- Browser UI backed by `internal/web/server.go`

Phase 2 should extend those capabilities without forcing a protocol rewrite.

## Phase 2 Scope

Phase 2 is limited to four deliverables:

1. `mDNS` service advertisement for hub discovery on the LAN
2. Raspberry Pi Zero 2W deployment profile and operational guidance
3. Cross-restart restore for stored files and paused uploads within a bounded TTL window
4. Chunk-size observability and manual tuning support

## Implementation Verification (as of 2026-03-11)

This section reflects what is currently implemented in the repository.

### Deliverable status

- [x] 1. mDNS service advertisement for LAN discovery
    - [x] `internal/discovery/mdns.go` implemented
    - [x] `_lantern._tcp` advertised
    - [x] `_lantern-http._tcp` advertised when HTTP is enabled
    - [x] `EnableMDNS` and `MDNSServiceName` added to config defaults
    - [x] `-mdns` and `-mdns-name` serve flags wired in `cmd/lantern/main.go`
    - [x] advertiser started/stopped from serve lifecycle via context and shutdown path

- [x] 2. Raspberry Pi Zero 2W deployment profile and guidance
    - [x] `PI_DEPLOYMENT.md` exists with ARM build command
    - [x] deployment steps documented
    - [x] example `systemd` unit documented
    - [x] recommended operating profile documented
    - [x] logging and troubleshooting commands documented

- [x] 3. Cross-restart restore (stored files + paused uploads)
    - [x] `internal/index/index.go` JSON index store implemented
    - [x] stored-file snapshots persisted and deleted on lifecycle transitions
    - [x] session snapshots persisted and deleted on lifecycle transitions
    - [x] startup restore wired via `server.New()` -> `restorePersistedState()`
    - [x] stored-file restore validates TTL/path before re-registration
    - [x] paused-session restore validates resume window
    - [x] hasher state rehydration from temp file implemented before resume

- [x] 4. Chunk-size observability and manual tuning support
    - [x] in-memory transfer stats collector implemented
    - [x] upload/download counts, bytes, duration, throughput captured
    - [x] CRC mismatch and resume counters captured
    - [x] `GET /api/stats` endpoint implemented
    - [x] `-chunk-size-kb` serve flag implemented
    - [x] average/recent chunk-size metrics implemented

### Test strategy status

- [x] `internal/discovery`: TXT/service construction test present (`mdns_test.go`)
- [x] `internal/index`: save/load round-trip test present (`index_test.go`)
- [x] `internal/server`: session restore/hasher rehydration test present (`persist_test.go`)
- [x] `internal/server/storage`: restore filtering behavior covered in `persist_test.go`
- [x] explicit tests for restore TTL-expiry and missing-temp-file restore paths present
- [x] explicit config-default coverage test for mDNS fields present (`config_test.go`)

### Definition of done snapshot

- [x] server advertises itself over mDNS
- [x] Pi deployment path documented and operationally described
- [x] completed stored files survive restart and remain downloadable
- [x] interrupted uploads can be restored server-side within bounded resume window
- [x] operators can inspect transfer stats and tune chunk size with complete evidence

Out of scope for this phase:

- automatic CLI-side mDNS discovery
- hotspot management / captive portal
- encrypted transport
- protocol redesign for full bidirectional resume negotiation

## 1. mDNS Discovery

### Goal

Allow devices on the LAN to discover the Lantern hub without typing the IP address manually.

### Proposed design

Add a new package:

- `internal/discovery/mdns.go`

Expose a small abstraction:

```go
type ServiceInfo struct {
    Instance string
    Host     string
    TCPPort  int
    HTTPPort int
}

func StartAdvertiser(ctx context.Context, info ServiceInfo) (stop func(), err error)
```

Implementation notes:

- Advertise `_lantern._tcp` for the raw TCP service.
- Either advertise `_lantern-http._tcp` as a second service or include the HTTP port in TXT records on `_lantern._tcp`.
- Keep the implementation behind the `internal/discovery` package so the rest of the server is isolated from library choice.
- Drive shutdown from `context.Context` created in `cmd/lantern/main.go`.

### Config and CLI changes

Extend `internal/config/config.go` with:

- `EnableMDNS bool`
- `MDNSServiceName string`

Add serve flags in `cmd/lantern/main.go`:

- `-mdns`
- `-mdns-name`

Server startup flow:

1. Build the base `config.Config`
2. Create the TCP server with `server.New(cfg)`
3. Start the HTTP server if enabled
4. Start mDNS advertisement if `cfg.EnableMDNS` is true
5. Stop the advertiser on shutdown before process exit

### Testing

Automated:

- unit tests for service/TXT record construction
- config default coverage for mDNS fields

Manual:

- verify on another machine with `dns-sd -B _lantern._tcp`
- confirm that the advertised name resolves to the current TCP and HTTP ports

## 2. Raspberry Pi Zero 2W Deployment

### Goal

Define a supported low-resource operating profile for Lantern running as a small always-on hub.

### Proposed defaults

These values should be documented first and optionally promoted to a named config profile later:

- `MaxUploadConcurrency`: `2` to `3`
- `MaxDownloadConcurrency`: `4` to `6`
- `ChunkSize`: `128 KiB` to `256 KiB`
- `MaxFileSize`: `1 GiB` to `2 GiB` depending on storage
- `SessionTimeout`: keep short to reap abandoned uploads quickly
- `TTLDefault`: shorter than desktop defaults when SD-card capacity is limited

### Implementation

Phase 2 does not require a second config system. The source of truth remains `internal/config.Config`.

Recommended work:

- add missing serve flags for the config values operators actually need to tune on Pi
- keep defaults conservative for Pi guidance, but do not silently degrade desktop usage unless measurements justify it
- document build, copy, and service management separately in a Pi deployment guide

### Deliverables

- `PI_DEPLOYMENT.md` with:
  - ARM build command
  - deployment steps
  - example `systemd` unit
  - recommended config values
  - logging and troubleshooting commands

## 3. Cross-Restart Resume

### Goal

Survive a process restart for:

- stored-file metadata already moved into final storage
- paused or interrupted uploads whose temp files still exist and have not expired

### Constraints from the current codebase

Today:

- `SessionStore` is memory-only
- `StorageManager.files` is memory-only
- temp files survive on disk, but the server forgets them after restart
- uploads track `LastAckedSeq`, `ReceivedChunks`, and temp paths already in memory

That means the safest Phase 2 slice is:

1. persistent stored-file reload
2. persistent paused-session snapshots
3. server-side restoration of resumable uploads
4. client-side explicit resume command in a later follow-up if needed

### Proposed package

Add:

- `internal/index/index.go`

Suggested interface:

```go
type Store interface {
    SaveSession(*SessionSnapshot) error
    DeleteSession(id string) error
    LoadSessions() ([]*SessionSnapshot, error)

    SaveStoredFile(*StoredFileSnapshot) error
    DeleteStoredFile(id string) error
    LoadStoredFiles() ([]*StoredFileSnapshot, error)
}
```

Phase 2 storage format:

- simple JSON files under `~/.lantern/index/`
- one file for sessions, one file for stored files, or one file per record if simpler to update safely

The JSON approach is sufficient for this phase because the write rate is low and the deployment target is single-node.

### Snapshot model

Persist only serializable state. Do not persist live connections or hashers.

Suggested types:

```go
type SessionSnapshot struct {
    ID               string
    State            string
    CreatedAt        time.Time
    LastActivity     time.Time
    CurrentFileIndex uint32
    Files            []FileTransferSnapshot
}

type FileTransferSnapshot struct {
    FileIndex       uint32
    Metadata        protocol.FileMetadata
    TempPath        string
    StoredFileID    string
    ReceivedChunks  uint32
    LastAckedSeq    uint32
    State           string
}

type StoredFileSnapshot struct {
    ID            string
    Path          string
    Metadata      protocol.FileMetadata
    DownloadCount int32
    MaxDownloads  int
    ExpiresAt     time.Time
}
```

### Integration points

Persist snapshots on significant transitions:

- session created
- session paused
- file header accepted
- chunk acknowledged at a throttled cadence or only when pausing
- file finalized
- session completed or failed
- stored file added or deleted

Restore on startup:

1. load stored-file snapshots
2. re-register files whose paths still exist and whose TTL/download limits are still valid
3. load session snapshots
4. restore only uploads with temp files still present and `LastActivity + SessionTimeout` still valid
5. rehydrate them as paused sessions so the resume path can reuse existing server logic

### Wire protocol impact

Minimal Phase 2 path:

- keep current handshake/file-header/chunk/control framing
- reuse `SESSION_ID`
- keep cross-restart resume server-driven first

Likely follow-up:

- add a small explicit resume control exchange so the client can query last acknowledged sequence after restart

### Risks

- current hashing is incremental in memory; after restart the server cannot finalize SHA-256 without re-reading temp data
- the restore path must recompute the hasher state from the existing temp file before accepting resumed chunks
- snapshot writes must be atomic enough to survive power loss on Pi hardware

Because of those constraints, file restore should:

1. stat the temp file
2. derive received bytes/chunks from file size and chunk size
3. replay the existing temp file into a new SHA-256 hasher before resuming

## 4. Chunk-Size Tuning

### Goal

Make chunk-size changes measurable and safe, especially on Raspberry Pi Zero 2W.

### Proposed scope

Phase 2 should focus on observability and operator-controlled tuning, not adaptive production logic.

### Implementation

Add lightweight transfer stats to `internal/server/handler.go` and expose them via HTTP.

Suggested metrics:

- transfer count by direction
- bytes transferred
- wall-clock duration
- computed throughput
- CRC mismatch / NAK count
- resume count
- average and recent chunk sizes in use

Suggested HTTP endpoint:

- `GET /api/stats`

This can live in `internal/web/server.go` and return JSON backed by a small in-memory stats collector owned by the server.

### Config surfacing

Expose chunk size more clearly through `lantern serve`, for example:

- `-chunk-size-kb`

Also document the interaction between chunk size, memory footprint, retry cost, and Pi CPU overhead.

### Manual benchmark procedure

Document a repeatable procedure:

1. choose a fixed test file set
2. test `128 KiB`, `192 KiB`, and `256 KiB`
3. record throughput, retries, and CPU load
4. keep the highest stable value, not just the fastest single run

## Rollout Order

Implement in this order:

1. Persistent stored-file index and startup reload
2. mDNS advertiser
3. Pi deployment guide and serve-flag surfacing
4. Session snapshot persistence and restore
5. Transfer stats and chunk-size tuning support

This order isolates the highest-value restart-safety work first and keeps mDNS independent of session persistence.

## Test Strategy

### Unit tests

- `internal/discovery`: service description and TXT record generation
- `internal/index`: save/load round-trip using a temp directory
- `internal/server/storage`: restoring stored-file entries from snapshots
- session restore logic for TTL expiry and missing temp files

### Manual tests

- discover the service from another machine using Bonjour tooling
- restart the server after a completed upload and confirm files remain listed
- restart the server mid-upload and verify the paused upload survives within the TTL window
- run the server on Pi Zero 2W using the recommended profile and compare chunk-size settings under load

## Definition of Done

Phase 2 is complete when:

- the server can advertise itself over mDNS
- a Pi deployment path is documented and operational
- completed stored files survive restart and remain downloadable
- interrupted uploads can be restored server-side within a bounded resume window
- operators can inspect transfer stats and tune chunk size with evidence
