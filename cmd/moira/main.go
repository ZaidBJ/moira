package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/moira-alert/moira-alert/cmd"
	"github.com/moira-alert/moira-alert/database/redis"
	"github.com/moira-alert/moira-alert/logging/go-logging"
	"github.com/moira-alert/moira-alert/metrics/graphite/go-metrics"

	"github.com/moira-alert/moira-alert/notifier"
	"github.com/moira-alert/moira-alert/notifier/events"
	"github.com/moira-alert/moira-alert/notifier/notifications"
	"github.com/moira-alert/moira-alert/notifier/selfstate"

	"github.com/moira-alert/moira-alert/checker/worker"
)

var (
	configFileName         = flag.String("config", "moira.yml", "Path to configuration file")
	printVersion           = flag.Bool("version", false, "Print version and exit")
	printDefaultConfigFlag = flag.Bool("default-config", false, "Print default config and exit")
)

// Moira version
var (
	Version   = "unknown"
	GitHash   = "unknown"
	GoVersion = "unknown"
)

func main() {
	flag.Parse()
	if *printVersion {
		fmt.Println("Moira - alerting system based on graphite data")
		fmt.Println("Version:", Version)
		fmt.Println("Git Commit:", GitHash)
		fmt.Println("Go Version:", GoVersion)
		os.Exit(0)
	}

	config := getDefault()
	if *printDefaultConfigFlag {
		cmd.PrintConfig(config)
		os.Exit(0)
	}

	err := cmd.ReadConfig(*configFileName, &config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't read settings: %v\n", err)
		os.Exit(1)
	}

	log, err := logging.ConfigureLog(config.LogFile, config.LogLevel, "main")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't configure main logger: %v\n", err)
		os.Exit(1)
	}

	databaseSettings := config.Redis.GetSettings()

	// API
	apiLog, err := logging.ConfigureLog(config.API.LogFile, config.API.LogLevel, "api")
	if err != nil {
		log.Fatalf("Can't configure logger for API: %v\n", err)
	}

	apiServer := &APIServer{
		Config: config.API.getSettings(),
		DB:     redis.NewDatabase(apiLog, databaseSettings),
		Log:    apiLog,
	}

	if err = apiServer.Start(); err != nil {
		log.Fatalf("Can't start API: %v", err)
	}

	// Filter
	filterLog, err := logging.ConfigureLog(config.Filter.LogFile, config.Filter.LogLevel, "filter")
	if err != nil {
		log.Fatalf("Can't configure logger for Filter: %v\n", err)
	}

	filterServer := &Filter{
		Config: config.Filter.getSettings(),
		DB:     redis.NewDatabase(filterLog, databaseSettings),
		Log:    filterLog,
	}

	if err = filterServer.Start(); err != nil {
		log.Fatalf("Can't start Filter: %v", err)
	}

	// Notifier
	notifierLog, err := logging.ConfigureLog(config.Notifier.LogFile, config.Notifier.LogLevel, "notifier")
	if err != nil {
		log.Fatalf("Can't configure logger for Filter: %v\n", err)
	}

	notifierMetrics := metrics.ConfigureNotifierMetrics("notifier")

	notifierDB := redis.NewDatabase(notifierLog, config.Redis.GetSettings())

	notifierConfig := config.Notifier.getSettings()
	sender := notifier.NewNotifier(notifierDB, notifierLog, *notifierConfig, notifierMetrics)

	if err = sender.RegisterSenders(notifierDB, notifierConfig.FrontURL); err != nil {
		log.Fatalf("Can't configure senders: %s", err.Error())
	}

	selfState := &selfstate.SelfCheckWorker{
		Log:      notifierLog,
		DB:       notifierDB,
		Config:   *config.Notifier.SelfState.getSettings(),
		Notifier: sender,
	}
	if err = selfState.Start(); err != nil {
		log.Fatalf("SelfState failed: %v", err)
	}

	fetchEventsWorker := events.FetchEventsWorker{
		Logger:    notifierLog,
		Database:  notifierDB,
		Scheduler: notifier.NewScheduler(notifierDB, notifierLog, notifierMetrics),
		Metrics:   notifierMetrics,
	}
	fetchEventsWorker.Start()

	fetchNotificationsWorker := &notifications.FetchNotificationsWorker{
		Logger:   notifierLog,
		Database: notifierDB,
		Notifier: sender,
	}
	fetchNotificationsWorker.Start()

	// Checker
	checkerLog, err := logging.ConfigureLog(config.Checker.LogFile, config.Checker.LogLevel, "checker")
	if err != nil {
		log.Fatalf("Can't configure logger for Checker: %v\n", err)
	}
	checkerMetrics := metrics.ConfigureCheckerMetrics("checker")
	checkerWorker := &worker.Checker{
		Logger:   checkerLog,
		Database: redis.NewDatabase(filterLog, databaseSettings),
		Config:   config.Checker.getSettings(),
		Metrics:  checkerMetrics,
		Cache:    cache.New(time.Minute, time.Minute*60),
	}

	if err = checkerWorker.Start(); err != nil {
		log.Fatalf("Start Checker failed: %v", err)
	}

	if err = metrics.Init(config.Graphite.GetSettings()); err != nil {
		log.Error(err)
	}

	log.Infof("Moira Started (version: %s)", Version)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	log.Info(<-ch)

	if err := filterServer.Stop(); err != nil {
		log.Errorf("Can't stop Filer: %v", err)
	}
	log.Info("Filter stopped")

	// Stop Notifier
	selfState.Stop()
	fetchEventsWorker.Stop()
	fetchNotificationsWorker.Stop()
	notifierDB.DeregisterBots()
	log.Info("Notifier stopped")

	// Stop Checker
	if err := checkerWorker.Stop(); err != nil {
		log.Errorf("Stop Checker Failed: %v", err)
	}
	log.Info("Checker stopped")

	// Stop Api
	if err := apiServer.Stop(); err != nil {
		log.Errorf("Can't stop API: %v", err)
	}
	log.Info("API stopped")
	log.Infof("Moira Stopped (version: %s)", Version)
}
