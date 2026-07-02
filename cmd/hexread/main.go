// Command hexread is the HexRead CLI - a small, static, pure HTTP API client over the public /v1
// contract. All conversion happens server-side. The command tree lives in internal/cli.
package main

import "github.com/HexWorldEU/hexread-cli/internal/cli"

func main() { cli.Execute() }
