package main

import (
	"go.uber.org/fx"

	"apostas-go/internal/platform/fxmodules"
)

func main() {
	fx.New(fxmodules.Application()).Run()
}
