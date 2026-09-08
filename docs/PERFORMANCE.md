# Performance and verification measurements

Measured 2026-09-08 in the provided Linux arm64 container, Go 1.27.1, two available
Go CPUs. These are **loopback TLS and local filesystem** results, not measurements
of a live Tailnet. Filesystem latency, container contention, VPN routing and peer
hardware substantially affect actual throughput.

| Measurement | Observed result |
| --- | --- |
| 1 GiB full transfer, initial run | 29.05 s; 35.3 MiB/s |
| 3 GiB full transfer, initial run | 118.99 s; 25.8 MiB/s |
| 1 GiB refined implementation rerun | 21.26 s; 48.2 MiB/s |
| Sampled peak Go heap during 1 GiB rerun | 8.0 MiB, both peers in one process |
| SHA-256 over 256 KiB buffer | 1,822.8 MB/s; 0 allocations |
| 100-file directory traversal and manifest | 310 µs; 85,936 B/op |
| 5-byte file, new mutual TLS plus durable journals | 29.5 ms; 676,600 B/op |
| Decode one 256 KiB binary data frame | 6,223.7 MB/s; 262,200 B/op |

The initial 1 GiB and 3 GiB tests overlapped compilation and race tests. The
rerun reduced that contention; these numbers should not be interpreted as a
measured optimization delta. Large sources were sparse zero-filled files;
receivers wrote all payload bytes to ordinary files through real TCP/TLS.
The heap figure samples `runtime.MemStats.HeapAlloc` every 10 ms, and is not peak
RSS or a claim that all kernel buffers/page cache fit in that amount.

A regular automated test throttles a 1 MiB transfer to a simulated 2 MiB/s
connection and verifies the resulting content (569 ms observed). Other tests exercise interrupted
TLS transfers, retained prefix length, engine restart and resume, cancellation,
trust revocation during streaming, changed source files, checksum failure,
malformed framing, invalid manifests, Unicode names, collision policies and
publication-journal crash recovery. These replace no claims about unavailable
real desktop or remote Tailnet environments.

## Reproduce

```sh
go test ./internal/identity ./internal/protocol ./internal/transfer
go test -race ./internal/identity ./internal/protocol ./internal/transfer
go test -run '^$' -bench . -benchmem ./internal/protocol ./internal/transfer
RELAY_LARGE_TEST=1 go test -run TestLargeFiles -v ./internal/transfer
go test -run 'TestSlowConnection|TestResumeAcrossEngineRestart' -v ./internal/transfer
```

The opt-in large suite transfers 1 GiB and 3 GiB and can require several GiB of
free destination space. It uses temporary directories and cleans them up. Run
`-run TestLargeFiles/1GiB` to select only the smaller case. Record Go version,
filesystem, free memory, CPU, concurrent load and route type before comparing
results. V1 deliberately avoids compression and speculative concurrency tuning;
its bounded streaming and durable finalization are the current baseline.
