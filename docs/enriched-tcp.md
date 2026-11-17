# Enriched TCP v1

This transport is selected explicitly using `transport: "enriched"`. It is not
compatible with a raw backend listener. Each TCP connection carries one call.
All frames use:

```text
TYPE: uint8 | PAYLOAD_LENGTH: uint32 big-endian | PAYLOAD: bytes
```

The length excludes the five-byte header. Maximum payload length is 65536 bytes.
Implementations must handle fragmented headers/payloads and coalesced frames.
No text delimiters, base64 encoding, or AT commands are used.

| Type | Direction | Payload |
|---|---|---|
| `0x01` START | Bridge → backend | UTF-8 JSON metadata |
| `0x02` DATA | Either direction | Nonempty unmodified binary bytes |
| `0x03` END | Bridge → backend | UTF-8 JSON `{ "reason": "..." }` |
| `0x04` HANGUP | Backend → bridge | Empty |

Example START payload:

```json
{
  "version": 1,
  "session_id": "4c6b71cd83dbed48def9a823",
  "protocol": "millennium",
  "connect_result": "CONNECT 1200",
  "connected_at": "2026-11-17T12:00:00Z"
}
```

The backend must check `version`; v1 has no in-band negotiation or client hello.
The first bridge frame is START, followed by DATA containing the buffered opening
bytes and then further DATA. Frame boundaries do not represent payphone packet
boundaries. `connect_result` is the modem's reported string, not independently
verified modulation metadata. Unknown JSON fields can be ignored.

HANGUP requests physical hangup. Socket closure also terminates the call. END is
best-effort: on abrupt transport failure, process shutdown, or a cancelled
session it may not be delivered. EOF must always be handled as session end.
The reason is diagnostic text, not a stable machine-readable error code.

Invalid types/directions, oversized frames, empty DATA, and nonempty HANGUP
terminate the session. Input is bounded before allocation. Socket writes have
a five-second deadline. There is no reconnection or session resumption.
