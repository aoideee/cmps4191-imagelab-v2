package main

import (
	"context"
	"database/sql"
	"flag"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/lewisdalwin/gatekeeper/internal/data"
	_ "github.com/lib/pq"
)

type config struct {
	port               int
	env                string
	jobDelay           time.Duration
	simulationInterval time.Duration
	simulationDuration time.Duration
	workerPollInterval time.Duration
	db                 struct {
		dsn          string
		maxOpenConns int
		maxIdleConns int
		maxIdleTime  time.Duration
	}
}

type application struct {
	config       config
	logger       *slog.Logger
	models       data.Models
	wg           sync.WaitGroup
	workerCancel context.CancelFunc
	observers    *observationHub
}

func main() {
	var cfg config

	var legacyDelay time.Duration
	flag.IntVar(&cfg.port, "port", 4000, "API server port")
	flag.StringVar(&cfg.env, "env", "development", "Environment (development|staging|production)")
	flag.DurationVar(&cfg.jobDelay, "job-delay", 0, "Quiet delay before processing (use 30s for condition A)")
	flag.DurationVar(&legacyDelay, "report-delay", 0, "Legacy alias for -job-delay")
	flag.DurationVar(&cfg.simulationInterval, "simulation-interval", 0, "Simulated update interval; zero disables simulation")
	flag.DurationVar(&cfg.simulationDuration, "simulation-duration", 10*time.Second, "Duration of the simulated update phase")
	flag.DurationVar(&cfg.workerPollInterval, "worker-poll-interval", 250*time.Millisecond, "Worker queue-check interval")

	flag.StringVar(&cfg.db.dsn, "db-dsn", "", "PostgreSQL DSN")

	flag.IntVar(&cfg.db.maxOpenConns, "db-max-open-conns", 25, "PostgreSQL max open connections")
	flag.IntVar(&cfg.db.maxIdleConns, "db-max-idle-conns", 25, "PostgreSQL max idle connections")
	flag.DurationVar(&cfg.db.maxIdleTime, "db-max-idle-time", 15*time.Minute, "PostgreSQL max connection idle time")

	flag.Parse()

	if cfg.jobDelay == 0 && legacyDelay > 0 {
		cfg.jobDelay = legacyDelay
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	db, err := openDB(cfg)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	defer db.Close()

	logger.Info("database connection pool established")

	app := &application{
		config:    cfg,
		logger:    logger,
		models:    data.NewModels(db),
		observers: newObservationHub(),
	}
	logger.Info(
		"experiment configuration",
		"quiet_delay", cfg.jobDelay,
		"simulation_interval", cfg.simulationInterval,
		"simulation_duration", cfg.simulationDuration,
	)

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	app.workerCancel = cancelWorker
	defer cancelWorker()
	// app.startReportWorker(workerCtx)
	app.startImageWorker(workerCtx)

	err = app.serve()
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

func openDB(cfg config) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.db.dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(cfg.db.maxOpenConns)
	db.SetMaxIdleConns(cfg.db.maxIdleConns)
	db.SetConnMaxIdleTime(cfg.db.maxIdleTime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = db.PingContext(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
