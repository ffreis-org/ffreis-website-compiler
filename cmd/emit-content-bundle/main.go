package main

import (
	"os"

	"ffreis-website-compiler/internal/bundlecmd"
	"ffreis-website-compiler/internal/logx"
)

func main() {
	logger := logx.New("emit-content-bundle")
	if err := bundlecmd.Run(os.Args[1:], logger); err != nil {
		logger.Error("emit-content-bundle failed", "error", err)
		os.Exit(1)
	}
}
