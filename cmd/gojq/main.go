// gojq - Go implementation of jq
package main

import (
	"os"

	"github.com/speakeasy-api/jq/cli"
)

func main() {
	os.Exit(cli.Run())
}
