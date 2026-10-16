package fxmodules

import (
	"io"
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"apostas-go/internal/platform/config"
	"apostas-go/internal/platform/logging"
)

// stopMargin é o que o Fx espera além do ShutdownTimeout, para o fechamento do pool do banco,
// que roda depois do desligamento das entradas e dos workers.
const stopMargin = 5 * time.Second

// Application é o serviço completo, configurado pelo ambiente do processo. Uma configuração
// inválida vira um erro do Fx, que o imprime e encerra o processo sem subir nada.
func Application() fx.Option {
	loaded, err := config.FromEnvironment()
	if err != nil {
		return fx.Error(err)
	}

	return ApplicationWith(loaded, os.Stdout)
}

// ApplicationWith monta o serviço com uma configuração já pronta; os testes a usam com
// portas e filas próprias. Os logs do serviço e os eventos do próprio Fx saem em JSON em
// logOutput.
func ApplicationWith(loaded config.Config, logOutput io.Writer) fx.Option {
	logger := logging.New(logOutput, loaded.LogLevel)

	return fx.Options(
		fx.Supply(loaded, logger),
		fx.StopTimeout(loaded.HTTP.ShutdownTimeout+stopMargin),
		fx.WithLogger(func() fxevent.Logger { return newQuietFxLogger(logger) }),
		PlatformModule,
		RepositoryModule,
		ServiceModule,
		ControllerModule,
	)
}
