package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/vito/bass"
	"github.com/vito/bass/ioctx"
	"github.com/vito/bass/prog"
	"github.com/vito/bass/zapctx"
	"golang.org/x/sync/errgroup"
)

func run(ctx context.Context, scope *bass.Scope, filePath string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}

	defer file.Close()

	recorder := prog.NewRecorder()
	ctx = prog.RecorderToContext(ctx, recorder)

	eg := new(errgroup.Group)

	eg.Go(func() error {
		// start reading progress so we can initialize the logging vertex
		return recorder.Display("Playing", os.Stderr)
	})

	logVertex := recorder.Vertex("log", "[bass]")
	stderr := logVertex.Stderr()

	// wire up logs to vertex
	logger := bass.LoggerTo(stderr)
	ctx = zapctx.ToContext(ctx, logger)

	// wire up stderr for (log), (debug), etc.
	ctx = ioctx.StderrToContext(ctx, stderr)

	// go!
	eg.Go(func() error {
		defer recorder.Close()
		_, err := bass.EvalReader(ctx, scope, file)
		return err
	})

	return eg.Wait()
}
