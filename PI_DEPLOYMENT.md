# Lantern on Raspberry Pi Zero 2W

This guide documents a conservative deployment profile for running Lantern as a small always-on LAN hub on Raspberry Pi Zero 2W hardware.

## Goals

- keep memory usage predictable
- avoid saturating the Pi with too many concurrent transfers
- keep SD-card usage bounded
- make startup and log inspection simple with `systemd`

## Recommended Operating Profile

Start with these values and tune only after measuring throughput and retry rates:

| Setting | Recommended value | Why |
|---|---|---|
| Upload concurrency | `2` or `3` | Limits memory and disk contention |
| Download concurrency | `4` to `6` | Keeps browser downloads responsive without overloading CPU |
| Chunk size | `128 KiB` to `256 KiB` | Balances syscall overhead and memory footprint |
| Default TTL | `900` to `1800` seconds | Prevents storage from filling with abandoned files |
| Max file size | `1 GiB` to `2 GiB` | Safer for SD-card-backed storage |

## Build for ARM

Build from a faster development machine:

```bash
GOOS=linux GOARCH=arm GOARM=7 go build -o lantern ./cmd/lantern
```

Copy to the Pi with `scp` or `rsync`:

```bash
scp ./lantern pi@raspberrypi.local:/home/pi/lantern/
```

## Suggested Directory Layout

```text
/home/pi/lantern/
  lantern
  storage/
  tmp/
```

If you add persistent session and file indexes in Phase 2, keep them under:

```text
/home/pi/.lantern/index/
```

## Running Manually

Use explicit paths so service behavior is predictable:

```bash
/home/pi/lantern/lantern serve \
  -addr 0.0.0.0 \
  -port 9723 \
  -http-port 9724 \
  -max-concurrent 2 \
  -storage /home/pi/lantern/storage \
  -temp /home/pi/lantern/tmp
```

If mDNS is enabled in Phase 2, add:

```bash
  -mdns \
  -mdns-name Lantern
```

When chunk-size tuning is surfaced on the CLI, prefer starting at:

```text
128 KiB
```

and only moving upward after measurement.

## systemd Service

Example unit:

```ini
[Unit]
Description=Lantern LAN file hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi/lantern
ExecStart=/home/pi/lantern/lantern serve -addr 0.0.0.0 -port 9723 -http-port 9724 -max-concurrent 2 -storage /home/pi/lantern/storage -temp /home/pi/lantern/tmp
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Install and enable:

```bash
sudo cp lantern.service /etc/systemd/system/lantern.service
sudo systemctl daemon-reload
sudo systemctl enable --now lantern.service
```

Check logs:

```bash
sudo journalctl -u lantern.service -f
```

Check status:

```bash
sudo systemctl status lantern.service
```

Stop or disable:

```bash
sudo systemctl stop lantern.service
sudo systemctl disable lantern.service
```

## Storage Guidance

- Prefer a good-quality SD card or USB storage if uploads are frequent.
- Keep `storage` and `tmp` on the same filesystem so temp-to-final moves remain cheap.
- Use shorter TTLs on small cards.
- Avoid treating Pi Zero 2W as permanent archival storage.

## Benchmarking Chunk Size

Use the same file set for each run and record:

- upload throughput
- download throughput
- retry count / CRC mismatches
- CPU saturation
- any stalled or failed transfers

Recommended sequence:

1. test `128 KiB`
2. test `192 KiB`
3. test `256 KiB`
4. choose the highest stable setting, not the peak burst result

## Operational Notes

- Lantern currently shares storage between TCP and HTTP flows, so both clients see the same uploaded files.
- In-memory session resume already works while the process stays alive.
- Cross-restart resume requires the persistent index work described in [PHASE2_PLAN.md](/Users/harishharish/Projects/Lantern/Lantern/PHASE2_PLAN.md).
