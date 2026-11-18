# ModemBridge

ModemBridge answers a call on a physical modem, identifies the payphone's
protocol and forwards the original bytes to a backend over TCP.

Despite the advent of LLM-generated softmodems, which partially address this
issue, there is still a use case for ModemBridge: if you have access to a POTS
line, you may prefer to use a hardware modem, to increase transport reliability.

```text
Payphone → telephone line → modem → ModemBridge → Server of your choice
```
## Supported protocols

| Payphone type | Protocol | Description |
|---------------|----------|-------------|
| Millennium | Bell 212A | Detected using the known start/stop bytes `02`& `03` and CRC16 at end |
| Jatena | V.21 | Detected using the known start/stop bytes `01` & `03`, control bytes, and FCC checksum at end |


## Requirements

- Go 1.24 or newer
- A serial modem and permission to access its device
- The backend for the phone you want to use

## Configure and run ModemBridge

From this repository:

```sh
go build -o modembridge ./cmd/modembridge
cp examples/modembridge.json modembridge.json
./modembridge probe /dev/ttyACM0
```

Replace `/dev/ttyACM0` with your modem device. Probe only an idle modem; it
changes modem settings and toggles DTR. Stop any other program using it first.

Edit `modembridge.json`: set the serial device, modem initialization commands,
flow control and backend addresses. Remove unused routes. The example contains
a USR5637 starting point, not a verified configuration for every modem or
phone. Make sure the host and modem agree on flow control.

```sh
./modembridge -check -config modembridge.json
./modembridge -config modembridge.json
```

Start the backends before placing a call. ModemBridge answers after the
configured number of rings, detects the phone and connects to the matching
route. Press Ctrl+C to stop each foreground process.

The complete example is [`examples/modembridge.json`](examples/modembridge.json).
Unknown fields, duplicate protocol routes and invalid limits are rejected.
`-check` validates the file without opening a modem or network socket.

Each raw TCP connection carries one call's original binary frames. The existing
backends use `"transport": "raw"`; the optional enriched transport is described
in [docs/enriched-tcp.md](docs/enriched-tcp.md).

## Call lifecycle and TCP contracts

The bridge opens the serial port, initializes the modem and waits for rings.
After `CONNECT`, it buffers the opening bytes and checks enabled protocol
detectors. Unknown or ambiguous calls are rejected. Once a route is selected,
the bridge attaches its backend, forwards the complete buffered opening, then
passes bytes in both directions until an endpoint closes or a limit is reached.
It does not acknowledge phone frames or translate their contents.

Each raw TCP connection carries one call's original binary frames. The existing
backends use `"transport": "raw"`; the optional enriched transport is described
in [docs/enriched-tcp.md](docs/enriched-tcp.md).
Raw TCP is a byte stream: read and write boundaries do not necessarily match
phone frame boundaries. Socket closure terminates the call.
TCP connections have no built-in authentication or encryption; use loopback or
a trusted network, or provide a secure tunnel.

To add another protocol, see [docs/adding-protocols.md](docs/adding-protocols.md).

## Start Millennium Manager

If you use Millennium phones, start the
[mm_manager fork](https://github.com/vekejsn/mm_manager) separately:

```sh
mm_manager -T 127.0.0.1:27274 -n YOUR_MANAGER_NUMBER
```

Replace `YOUR_MANAGER_NUMBER` with your manager number. Remove the Millennium
route from the bridge configuration if you don't use that backend.


## Start Jatena Server

TODO.
