# hexread CLI

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

`hexread` is the official command-line client for **[HexRead](https://hexread.com)** -
convert PDFs and images to Markdown (+JSON). The CLI is a thin, static API client: it talks
to the HexRead API at `https://api.hexread.com/v1` and never processes documents locally.

## Install

| Method | Command |
|---|---|
| **curl \| sh** (Linux/macOS) | `curl -fsSL https://hexread.com/install \| sh` |
| **Homebrew** (macOS/Linux) | `brew install HexWorldEU/tap/hexread` |
| **Scoop** (Windows) | `scoop bucket add hexread https://github.com/HexWorldEU/scoop-bucket && scoop install hexread` |
| **winget** (Windows) | `winget install HexWorld.HexRead` |
| **go install** | `go install github.com/HexWorldEU/hexread-cli/cmd/hexread@latest` |
| **Manual** | Download from [Releases](https://github.com/HexWorldEU/hexread-cli/releases), verify, extract |

> **Pre-release:** HexRead hasn't launched yet - until the first tagged release, install
> from source: `go install github.com/HexWorldEU/hexread-cli/cmd/hexread@main`.

The `curl | sh` installer always verifies each archive's SHA-256 against the release's
checksums file, and verifies the checksums file's **cosign** signature (keyless, GitHub
OIDC) when cosign is installed - pass `--require-cosign` (or set
`HEXREAD_REQUIRE_COSIGN=1`) to make the signature check mandatory. Any verification
mismatch aborts before anything is installed. `go install` builds the latest tagged
release from source (Go 1.25+).

Supported platforms: **Linux, macOS, Windows** on **amd64** and **arm64**.

### Verify a download manually

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/HexWorldEU/hexread-cli/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  checksums.txt
```

## Quick start

```sh
# Create an API key in your HexRead dashboard, then:
hexread login --key-stdin                 # paste the key; stored 0600 (or OS keychain)

hexread convert report.pdf -o report.md   # convert a single file
hexread convert - < scan.png              # stdin → Markdown on stdout
hexread batch './docs/*.pdf' -o ./md      # convert many files in parallel
hexread watch ./inbox -o ./md             # convert files as they appear
hexread usage                             # page usage / allowance + concurrency
hexread keys list                         # manage API keys
hexread version                           # CLI version
```

`hexread --help` lists every command. In CI, set `HEXREAD_API_KEY` instead of storing a
credential on disk.

## Configuration

Precedence: **flags > environment > config file > defaults**.

| Environment variable | Meaning |
|---|---|
| `HEXREAD_API_KEY` | API key for this invocation (nothing written to disk) |
| `HEXREAD_BASE_URL` | API base URL (default `https://api.hexread.com/v1`) |
| `HEXREAD_KEYRING` | credential store: `file` (default, a 0600 file) or `system` (OS keychain) |

Optional config file `~/.config/hexread/config.yaml` (flat `key: value` lines):

```yaml
base-url: https://api.hexread.com/v1
quiet: false
```

## Exit codes

Errors map to stable, scriptable exit codes that mirror the API's error classes:

| Exit | Meaning |
|---|---|
| 0 | success |
| 1 | generic / network / server error |
| 2 | usage error (bad flags/arguments) |
| 3 | authentication failed |
| 4 | forbidden (tier/permissions) |
| 5 | quota / page allowance exceeded |
| 6 | rate limited |
| 7 | unprocessable (validation, not found, conflict, gone) |
| 8 | payload too large |
| 9 | capacity (transient - retry later) |
| 10 | partial batch failure (some files failed) |
| 130 | interrupted (Ctrl-C) |

## License

Licensed under the [MIT License](LICENSE), © HexWorld Solutions GmbH.

The HexRead service and its documentation live at [hexread.com](https://hexread.com);
this repository contains the open-source CLI only.
