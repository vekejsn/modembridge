# ModemBridge

A Linux-first dial-up gateway that identifies incoming payphone sessions and
bridges their original binary data to the appropriate backend service.

```text
Payphone → telephone line → hardware modem → ModemBridge
                                               ├─ TCP → Millennium Manager
                                               └─ TCP → Jatena server
```

One physical modem handles one call at a time. The modem demodulates the line;
ModemBridge identifies the resulting protocol bytes. It does not implement a
software modem or translate payphone messages.

## Implemented

- Exclusive Linux serial access, 8N1, configurable DTE baud rate and RTS/CTS.
- Ordered, user-configurable initialization commands with expected responses
  and timeouts. A `probe` command queries identity and suggests a USR5637 base.
- Incoming rings, manual answering, negotiation timeout, optional DCD monitoring,
  DTR hangup, and reopening/reinitialization after each call or hardware error.
- Passive Millennium length/CRC-16 detection and Jatena framing/FCC detection.
  Only configured profiles participate; ambiguous/unknown calls are rejected.
- Exact initial-byte replay, binary bidirectional forwarding, bounded buffering,
  idle/session/write deadlines, and shutdown on either endpoint's disconnect.
- TCP connect and listen modes; raw and enriched transports.
- JSON session logs and raw TCP integration with the Jatena backend.

**Hardware validation is still required.** The USR5637 is the initial target, but
no physical modem or payphone was used to validate this implementation. In
particular, one initialization configuration answering both Bell 212A and V.21
has not been verified. The detectors follow the linked backends; real opening
captures are needed to establish compatibility across phone variants.

## Build and run

Requires Go 1.24 or newer. From this repository:

```sh
go build -o /tmp/modembridge ./cmd/modembridge
/tmp/modembridge -check -config examples/modembridge.json
/tmp/modembridge probe /dev/ttyACM0
/tmp/modembridge -config examples/modembridge.json
```

Edit a copy of `examples/modembridge.json` for your serial device and backends.
Give the service account access to that serial device. Stop any other service
that owns the modem. Only run `probe` on an idle modem: it changes result-code
settings, asserts DTR, and drops DTR on exit.

Start Millennium Manager from the [referenced fork](https://github.com/vekejsn/mm_manager):

```sh
mm_manager -T 127.0.0.1:27274 -n YOUR_MANAGER_NUMBER
```

The Jatena backend lives in the sibling checkout `../govorilnice-server`.
Build and start it from that checkout, using its normal data directory:

```sh
make
./main --listen 127.0.0.1:27275
```

Both backends listen; ModemBridge connects to the selected backend after
classification. Start them before placing a call.

## Configuration

Unknown keys, unsupported profiles, duplicate routes, and invalid limits are
rejected. `-check` validates without opening devices or sockets.

### Physical modem

| Setting | Default / meaning |
|---|---|
| `device` | Required serial path; `/dev/serial/by-id/...` is useful for stable names |
| `baud` | 19200; host-to-modem rate, **not** telephone-line rate |
| `hardware_flow_control` | `true`; Linux RTS/CTS; configure the modem consistently |
| `write_timeout_seconds` | 30; maximum duration of an individual serial write |
| `monitor_carrier` | `true`; terminate on DCD loss; requires normal carrier reporting |
| `answer_after_rings` | 1; the service issues `ATA` |
| `negotiation_seconds` | 45 |
| `init` | Ordered `{send, expect, timeout_seconds}` objects; default expectation `OK`, timeout 3 seconds |

Commands exclude the terminating carriage return. The service first issues
`ATQ0V1`, runs the configured commands, then issues `ATE0Q0V1S0=0` so that readable
results and manual answering remain under its control. Initialize the modem for
DTR hangup (`&D2` on the USR5637) and real DCD reporting (`&C1`). DTR support is
required even when carrier monitoring is disabled. There is no `+++` fallback.

The example's USR5637 base commands enable a fixed DTE rate and disable modem
error correction/compression. They are **not a verified universal modulation
configuration**. Bell 212A (`B1`), V.21 (`B15`), Bell 103 (`B16`), and V.23
(`S37=2`) are documented USR5637 choices to investigate separately. Commands
from another modem's backend initialization must not be copied indiscriminately.

### Routes

```json
{
  "protocol": "jatena",
  "mode": "connect",
  "address": "127.0.0.1:27275",
  "transport": "raw"
}
```

- `protocol`: `millennium` or `jatena`.
- `mode`: `connect` (default) dials the backend; `listen` exposes a persistent
  TCP listener that a backend can connect to. A connection is accepted when a
  matching call is ready. Use one backend client per endpoint; connections are
  not multiplexed or reusable across calls.
- `transport`: `raw` (default) or `enriched`; the existing backends use `raw`.
- `address`: explicit TCP host and port, including `[::1]:27275` for IPv6.

Use local endpoints unless remote access is intended. TCP has no built-in
authentication or encryption.

Top-level timeouts and limits are shown in the example. During identification,
all bytes count toward `max_identification_bytes` (default 65536); the service
does not consume or acknowledge any phone frame. A backend must attach within
`backend_seconds`. Input queues remain bounded while it attaches. The backend
must respond within the phone's own protocol deadline, regardless of these
configured limits.

## TCP contracts

**Raw:** one TCP connection per call; all bytes, including phone framing,
escaping, and checksums, are forwarded unchanged. There is no AT exchange or
metadata. Physical disconnect closes TCP; backend closure hangs up the call.
TCP write boundaries have no protocol significance.

**Enriched:** see [the v1 wire format](docs/enriched-tcp.md). It adds session
metadata and explicit hangup control while keeping data frames binary.

## Verification

```sh
go test -race ./...
go vet ./...
```

The tests include successive calls through a simulated AT modem and separate
TCP backends, initial-byte replay, binary forwarding, DTR cleanup, detector
checksums/ambiguity, limits/timeouts, and enriched TCP framing. The sibling Jatena
checkout includes source-derived full-dialogue tests for the C backend,
two wire profiles, and explicit error/retry policies. Its default drops malformed
frames and does not transmit NAKs or automatically retransmit table blocks.
See `../govorilnice-server/docs/compatibility.md` for how the
newer phone source explains the deployed backend's behavior and completion.

## Jatena readable output and table tools

The Jatena backend prints timestamped frame summaries, named status/statistics,
card/call records, decoded configuration, and transfer progress. Select
`--log-level summary`, `frames` (default), or `hex` for hex/ASCII dumps.

The backend's Python tools create and inspect raw SPD/TAR files. From
`../govorilnice-server`:

```sh
python3 tools/jatena.py spd template -o /tmp/spd.json
python3 tools/jatena.py spd build /tmp/spd.json -o /tmp/spd.bin
python3 tools/jatena.py tar build examples/jatena-tar.json -o /tmp/tar.bin
python3 tools/jatena.py tar lookup /tmp/tar.bin 001234567
python3 tools/jatena.py decode status /path/to/phone/status.bin
```

The backend checkout also ships a standalone terminal manager web UI
(`python3 -m webui --data-dir ./data`) that manages per-phone SPD/TAR bundles
and imports call/status history through the shared data directory; see its
README for deployment notes and token authentication.

Linux amd64, arm64, and ARMv7 builds are intended targets. Cross-compilation is
supported without CGO; hardware and driver behavior must be tested on each
deployment. macOS remains experimental; the portable fallback does not support
hardware flow control. Windows is not a supported target.

## Next compatibility work

- Verify the USR5637's mixed-mode answering with alternating real calls.
- Record real Jatena/Millennium opening exchanges and timing requirements.
- Add Protel after obtaining protocol documentation or captures.
- Verify V.23 with initial server TX 1200 / RX 75. Channel reversal depends on
  modem capabilities; no reversal control is implemented yet.
- Add virtual AT-modem endpoints when another backend needs them.

References: [USR5637 commands](https://www.usr.com/support/5637/5637-ug/ref_data.html),
[Millennium backend](https://github.com/vekejsn/mm_manager),
[Jatena backend](https://gitlab.com/derp-si/govorilnice-server/).
