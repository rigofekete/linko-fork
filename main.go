package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	logger, cleanUp, err := initializeLogger(os.Getenv("LINKO_LOG_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger %v", err)
		return 1
	}
	defer func() {
		if err := cleanUp(); err != nil {
			fmt.Fprintf(os.Stderr, "error cleaning up %v", err)
		}
	}()

	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Info(fmt.Sprintf("Error initializing store %v", err))
		return 1
	}

	s := newServer(*st, httpPort, logger, cancel)
	logger.Debug(fmt.Sprintf("Linko is running on http://localhost:%d", httpPort))
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logger.Debug("Linko is shutting down")
	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error(fmt.Sprintf("failed to shutdown server: %v", err))
		return 1
	}
	if serverErr != nil {
		logger.Error(fmt.Sprintf("server error: %v", serverErr))
		return 1
	}
	return 0
}

type closeFunc func() error

func initializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	replaceAttr := func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == "error" {
			err, ok := a.Value.Any().(error)
			if !ok {
				return a
			}
			return slog.String("error", fmt.Sprintf("%+v", err))
		}
		return a
	}

	handlers := []slog.Handler{
		slog.NewTextHandler(
			os.Stderr, &slog.HandlerOptions{
				Level:       slog.LevelDebug,
				ReplaceAttr: replaceAttr,
			}),
	}

	closers := []closeFunc{}
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("error opening file %w", err)
		}
		bufferedFile := bufio.NewWriterSize(file, 8192)
		infoHandler := slog.NewJSONHandler(
			bufferedFile,
			&slog.HandlerOptions{
				Level:       slog.LevelInfo,
				ReplaceAttr: replaceAttr,
			})
		handlers = append(handlers, infoHandler)

		close := func() error {
			if err := bufferedFile.Flush(); err != nil {
				return fmt.Errorf("error flushing buffered file %w", err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("error closing file %w", err)
			}
			return nil
		}
		closers = append(closers, close)
		closer := func() error {
			var errs []error
			for _, close := range closers {
				if err := close(); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		}
		return slog.New(slog.NewMultiHandler(handlers...)), closer, nil
	}
	return slog.New(slog.NewMultiHandler(handlers...)), func() error { return nil }, nil
}
