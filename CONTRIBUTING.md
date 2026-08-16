# Contributing

Issues and pull requests are welcome.

Before submitting a change:

1. Keep UI/configuration changes separated from real-time media changes where possible.
2. Run `gofmt`, `go test ./...`, `go test -shuffle=on ./...`, `go vet ./...`, and `go test -race ./...` from `reolink_sip_gateway/`.
3. For AEC/media changes, include the relevant before/after diagnostic metrics and state whether the change was hardware-tested.
4. Do not commit credentials, Home Assistant tokens, SIP passwords, public phone numbers, or private device identifiers.
5. Preserve the public 1-based NVR channel semantics unless an explicit migration is included.

For bug reports, include the shortest log section that reproduces the problem and redact secrets first.
