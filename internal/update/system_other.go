//go:build !linux

package update

import (
	"context"
	"errors"
)

func Serve(context.Context, string) error { return errors.New("online updates require Linux systemd") }
