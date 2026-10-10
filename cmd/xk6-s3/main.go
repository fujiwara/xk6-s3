// Command xk6-s3 is k6 with the xk6-s3 extension, as built by xk6.
package main

import (
	_ "github.com/fujiwara/xk6-s3"
	k6cmd "go.k6.io/k6/v2/cmd"
)

func main() {
	k6cmd.Execute()
}
