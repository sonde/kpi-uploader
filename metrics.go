package main

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	logger "github.com/sonde/logger"
)

const (
	namespace = "syncer"
)

var (
	syncStatusSynced    = "synced"
	syncStatusCollision = "collision"
	syncStatusFailed    = "failed"
)

var (
	errorCode = map[string]int{
		syncStatusSynced:    1,
		syncStatusCollision: 2,
		syncStatusFailed:    3,
	}
	logit = logger.Log

	// SyncRunDurationSeconds times the execution time of the update cycle
	SyncRunDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:      "sync_run_duration_seconds",
		Namespace: namespace,
		// avg. duration should be ~20s, initial sync may take ~120s
		Buckets: []float64{10, 15, 20, 25, 30, 40, 50, 60, 90, 120, 190, 240, 300},
		Help:    "Histogram for duration and total number of of sync runs",
	})

	// ReadEndpointData is the count of all secrets loaded from kubernetes
	ReadEndpointData = prometheus.NewCounter(prometheus.CounterOpts{
		Name:      "read_endpoint_data_total",
		Namespace: namespace,
		Help:      "total count of endpoint data from all sources",
	})
	// DataUploadedToSheet counts all Google sheet updates and writes
	DataUploadedToSheet = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:      "synced_datapoints_total",
			Namespace: namespace,
			Help:      "total count of datapoints to the Google spreadsheet",
		},
		[]string{"status"},
	)
)

func init() {
	prometheus.MustRegister(
		SyncRunDurationSeconds,
		ReadEndpointData,
		DataUploadedToSheet,
	)
}

func isAlive(w http.ResponseWriter, r *http.Request) {
	_, err := fmt.Fprint(w, "Alive.")
	if err != nil {
		logit.WithFields(log.Fields{
			"error": err,
		}).Error("responding with alive")

		//logger.Error("error when responding with alive", err)
	}
}

func isReady(w http.ResponseWriter, r *http.Request) {
	_, err := fmt.Fprint(w, "Ready.")
	if err != nil {
		logit.Error("error when responding with ready", err)
	}
}

// Serve metrics and health endpoints
func Serve(address, metrics, ready, alive string, log log.FieldLogger) {
	h := http.NewServeMux()
	h.Handle(metrics, promhttp.Handler())
	h.HandleFunc(ready, isReady)
	h.HandleFunc(alive, isAlive)
	logit.Infof("HTTP server started on %s", address)
	logit.Infof("serving metrics on %s", metrics)
	logit.Infof("serving readiness check on %s", ready)
	logit.Infof("serving liveness check on %s", alive)
	logit.Info(http.ListenAndServe(address, h))
}
