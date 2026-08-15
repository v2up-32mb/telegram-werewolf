package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return nil
}

func main() {
	if err := run(context.Background(), os.Args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
