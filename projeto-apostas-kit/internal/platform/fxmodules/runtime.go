package fxmodules

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/fx"

	"apostas-go/internal/controller/sqs"
	"apostas-go/internal/platform/config"
)

// Runtime é o conjunto que roda: servidor HTTP, consumidor SQS e workers. Ele concentra a
// ordem de início e de parada, que é parte do contrato do serviço:
//
//	início: workers, consumidor, servidor HTTP (só aceita tráfego com tudo no ar);
//	parada: servidor HTTP e consumidor (as entradas) em paralelo, depois os workers; o pool
//	        do banco fecha por último, em um hook anterior do Fx.
type Runtime struct {
	server     *http.Server
	consumer   *sqs.Consumer
	workers    Workers
	logger     *slog.Logger
	timeout    time.Duration
	shutdowner fx.Shutdowner

	mutex    sync.Mutex
	listener net.Listener
}

type runtimeInputs struct {
	fx.In

	Server     *http.Server
	Consumer   *sqs.Consumer
	Workers    Workers
	Logger     *slog.Logger
	Settings   config.Config
	Shutdowner fx.Shutdowner
}

func newRuntime(inputs runtimeInputs) *Runtime {
	return &Runtime{
		server:     inputs.Server,
		consumer:   inputs.Consumer,
		workers:    inputs.Workers,
		logger:     inputs.Logger,
		timeout:    inputs.Settings.HTTP.ShutdownTimeout,
		shutdowner: inputs.Shutdowner,
	}
}

func registerRuntime(lifecycle fx.Lifecycle, runtime *Runtime) {
	lifecycle.Append(fx.Hook{OnStart: runtime.start, OnStop: runtime.stop})
}

// Addr é o endereço em que o servidor HTTP escuta; vazio enquanto não iniciou.
func (runtime *Runtime) Addr() string {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()

	if runtime.listener == nil {
		return ""
	}

	return runtime.listener.Addr().String()
}

// start não bloqueia. Se uma etapa falha, o que já subiu é parado aqui mesmo, porque o Fx só
// executa o OnStop de hooks cujo OnStart deu certo.
func (runtime *Runtime) start(ctx context.Context) error {
	if err := runtime.startAll(ctx); err != nil {
		return errors.Join(err, runtime.stop(ctx))
	}

	return nil
}

func (runtime *Runtime) startAll(ctx context.Context) error {
	for _, running := range runtime.workers {
		// O contexto do OnStart vale só para a partida; o laço do worker deve sobreviver a ele.
		if err := running.Start(); err != nil { //nolint:contextcheck
			return fmt.Errorf("start worker: %w", err)
		}
	}

	if err := runtime.consumer.Start(); err != nil { //nolint:contextcheck
		return fmt.Errorf("start consumer: %w", err)
	}

	return runtime.listen(ctx)
}

func (runtime *Runtime) listen(ctx context.Context) error {
	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", runtime.server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", runtime.server.Addr, err)
	}

	runtime.mutex.Lock()
	runtime.listener = listener
	runtime.mutex.Unlock()

	go runtime.serve(listener)

	return nil
}

// serve roda até o Shutdown. Se o servidor cair por outro motivo, o serviço inteiro é
// parado: sem a borda HTTP, ele não pode continuar saudável.
func (runtime *Runtime) serve(listener net.Listener) {
	err := runtime.server.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}

	runtime.logger.Error("http server failed", slog.String("error", err.Error()))

	if shutdownErr := runtime.shutdowner.Shutdown(); shutdownErr != nil {
		runtime.logger.Error("shutdown request failed", slog.String("error", shutdownErr.Error()))
	}
}

// stop usa o prazo próprio de ShutdownTimeout, não o do Fx, para o fechamento do pool ainda
// caber no prazo do Fx (que tem uma margem a mais).
func (runtime *Runtime) stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtime.timeout)
	defer cancel()

	inputs := inParallel(
		func() error { return runtime.shutdownServer(ctx) },
		func() error { return runtime.consumer.Stop(ctx) },
	)

	stops := make([]func() error, 0, len(runtime.workers))
	for _, running := range runtime.workers {
		stops = append(stops, func() error { return running.Stop(ctx) })
	}

	return errors.Join(inputs, inParallel(stops...))
}

// shutdownServer espera as requisições em andamento até o prazo; vencido o prazo, fecha as
// conexões que restaram.
func (runtime *Runtime) shutdownServer(ctx context.Context) error {
	err := runtime.server.Shutdown(ctx)
	if err == nil {
		return nil
	}

	return errors.Join(err, runtime.server.Close())
}

func inParallel(steps ...func() error) error {
	results := make([]error, len(steps))

	var group sync.WaitGroup

	for index, step := range steps {
		group.Add(1)

		go func() {
			defer group.Done()

			results[index] = step()
		}()
	}

	group.Wait()

	return errors.Join(results...)
}
