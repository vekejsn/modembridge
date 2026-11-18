# Adding a protocol

Implement an opening-frame detector in `internal/protocol`. It receives the
buffered bytes from the phone and returns true only when they contain a valid
frame for the protocol. It must not modify the buffer or send any response.
Use sufficiently specific framing and checksum checks to avoid false matches.

Add the protocol name and detector function to the `detectors` registry in
`internal/protocol/detect.go`. The same registry controls configuration
validation and detection; no separate protocol list is needed.

Add tests for partial, valid and invalid frames, and for ambiguity with other
protocols. Configure a route using the registered name and a suitable backend.
The backend receives the original bytes, including those buffered during
identification. A protocol whose first exchange requires the bridge itself to
transmit may need additional implementation beyond a detector.
