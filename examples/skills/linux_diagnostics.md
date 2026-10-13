# Linux Diagnostics

Standard, low-risk procedures for checking the health of a Linux host.
Prefer these read-only commands before taking any corrective action.

## CPU & Load
- `uptime` — quick load-average snapshot (1/5/15 minute averages).
- `top -bn1 | head -20` — one-shot snapshot of top CPU consumers (use
  `-bn1` to avoid the interactive/full-screen mode, which would block a
  non-interactive agent).
- `mpstat 1 3` (if available) — per-core utilization over a few seconds.

## Memory
- `free -h` — human-readable memory and swap usage.
- `ps aux --sort=-%mem | head -10` — top memory-consuming processes.

## Disk
- `df -h` — filesystem usage; flag any mount above ~85% full.
- `du -sh <dir> 2>/dev/null | sort -rh | head -10` — find the largest
  subdirectories under a given path.
- `lsof +L1` (if available) — files deleted but still held open by a
  process (common cause of "disk full but df says there's space").

## Processes & Services
- `ps aux --sort=-%cpu | head -10` — top CPU-consuming processes.
- `systemctl --failed` — any failed systemd units.
- `journalctl -u <unit> --since "1 hour ago" --no-pager` — recent logs
  for a specific unit. Always use `--no-pager` (or rely on Litegent's
  built-in `PAGER=cat` override) so the command returns immediately
  instead of waiting for pager input.

## Network
- `ss -tulpn` — listening sockets and the owning process.
- `ip -brief addr` — interface addresses at a glance.

## Rule of thumb
Start broad (uptime, free, df) before narrowing to a specific process or
unit. Only take a mutating action (restarting a service, killing a
process) when the task explicitly calls for it.
