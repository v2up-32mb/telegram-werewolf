package main

import (
	"bytes"
	"context"
	"testing"
)

func TestRunSmoke(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	args := []string{"werewolf"}

	if err := run(ctx, args, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
}
