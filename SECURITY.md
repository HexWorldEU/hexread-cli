# Security Policy

## Reporting a vulnerability

Please report vulnerabilities **privately** - do not open a public issue.

- Preferred: [GitHub private vulnerability reporting](https://github.com/HexWorldEU/hexread-cli/security/advisories/new)
  ("Report a vulnerability" on this repository's Security tab).
- Email: security@hexread.com

You'll get an acknowledgement within 72 hours. Please include a reproduction where possible.

## Scope

This repository contains only the open-source `hexread` CLI - a pure API client. Issues in the
HexRead **service** (api.hexread.com, hexread.com) are also welcome through the same channels.

## Verifying releases

Every release is built by GitHub Actions and cosign-signed (keyless, GitHub OIDC). Verify a
download with the command in [README.md](README.md#verify-a-download-manually); the `curl | sh`
installer checks the SHA-256 always and the cosign signature when cosign is installed.
