package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/hashicorp/go-multierror"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/spf13/viper"
)

var queryParser = regexp.MustCompile(`^\s*SELECT\s+LAST\((?P<column>.+)\)\s+FROM\s+(?P<metric>\S+)\s+WHERE\s+(?P<where_key>[^\s=]+)=['"]?(?P<where_value>[^\s='"]+)['"]?\s*$`)

const (
	viperPort     = "port"
	viperUpstream = "upstream"
	viperLogLevel = "log_level"
)

func main() {
	ctx := context.Background()

	// Initialize viper
	v := viper.New()
	v.SetEnvPrefix("IUP")
	v.AutomaticEnv()

	// Set variable defaults
	v.SetDefault(viperPort, 8888)
	v.SetDefault(viperUpstream, "http://localhost:8428")
	v.SetDefault(viperLogLevel, "info")

	// Fetch the variable values
	port := v.GetInt(viperPort)
	upstream := v.GetString(viperUpstream)
	logLevelStr := v.GetString(viperLogLevel)

	logLevel := slog.LevelInfo
	if err := logLevel.UnmarshalText([]byte(logLevelStr)); err != nil {
		panic(fmt.Sprintf("Failed to parse log_level - %v", err))
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	// Start the server
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	shutdownFunc, err := StartServer(ctx, log, port, upstream)
	if err != nil {
		panic(fmt.Sprintf("Failed to start server - %v", err))
	}

	doneSignal := <-done
	log.Debug(fmt.Sprintf("Got %v signal", doneSignal))

	timeout := 10
	log.Info(fmt.Sprintf("HTTP Server Stopping... Waiting up to %d seconds for all in-progress requests to finish.", timeout))

	shutdownCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	if err := shutdownFunc(shutdownCtx); err != nil {
		log.Error("%v", err)
	}

	delay := 2
	log.Info(fmt.Sprintf("HTTP Server shutdown finished - will now delay %d seconds before exiting", delay))
	time.Sleep(time.Duration(delay) * time.Second)
	log.Info("Server Exited")
}

func StartServer(ctx context.Context, log *slog.Logger, port int, upstream string) (shutdownFunc func(ctx context.Context) error, err error) {
	router := mux.NewRouter()

	client, err := api.NewClient(api.Config{
		Address: upstream,
	})
	if err != nil {
		return nil, fmt.Errorf("Failed to create Prometheus querying client - %w", err)
	}

	upstreamURL, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse upstream address")
	}

	reverseProxy := httputil.NewSingleHostReverseProxy(upstreamURL)

	router.Handle("/query", &queryHandler{log: log, prometheusAPI: v1.NewAPI(client)})
	router.PathPrefix("/").Handler(reverseProxy)

	router.Use(loggingMiddleware)

	srv := &http.Server{
		Addr:        fmt.Sprintf(":%d", port),
		Handler:     router,
		BaseContext: func(_ net.Listener) context.Context { return ctx },
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("Failed to start listening server - %v", err)
		}
	}()

	log.Info("HTTP Server Started")

	shutdownFunc = func(shutdownCtx context.Context) error {
		shutdownErr := &multierror.Error{}

		if err := srv.Shutdown(shutdownCtx); err != nil {
			shutdownErr = multierror.Append(shutdownErr, fmt.Errorf("HTTP Server Shutdown Failed - %v", err))
		}
		// Close as well in case of context deadline - so we ensure connections are closed.
		if err := srv.Close(); err != nil {
			shutdownErr = multierror.Append(shutdownErr, fmt.Errorf("Failed closing server - %v", err))
		}

		return shutdownErr.ErrorOrNil()
	}

	return shutdownFunc, err
}

func loggingMiddleware(next http.Handler) http.Handler {
	return handlers.LoggingHandler(os.Stdout, next)
}

type parsedQuery struct {
	column     string
	metric     string
	whereKey   string
	whereValue string
}

func parseQuery(query string) (parsedQuery, error) {
	matches := queryParser.FindStringSubmatch(query)
	if matches == nil {
		return parsedQuery{}, fmt.Errorf("Invalid query format - Failed to match")
	}

	return parsedQuery{
		column:     matches[queryParser.SubexpIndex("column")],
		metric:     matches[queryParser.SubexpIndex("metric")],
		whereKey:   matches[queryParser.SubexpIndex("where_key")],
		whereValue: matches[queryParser.SubexpIndex("where_value")],
	}, nil
}

type queryHandler struct {
	log           *slog.Logger
	prometheusAPI v1.API
}

func (h *queryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse query data - %v", err), http.StatusInternalServerError)
		return
	}

	db, ok := r.Form["db"]
	if !ok {
		http.Error(w, "Missing `db` field in form data", http.StatusBadRequest)
		return
	}

	epoch, ok := r.Form["epoch"]
	if !ok {
		http.Error(w, "Missing `epoch` field in form data", http.StatusBadRequest)
		return
	}

	if epoch[0] != "s" {
		http.Error(w, "Invalid `epoch` field value in form data", http.StatusBadRequest)
		return
	}

	queryStr, ok := r.Form["q"]
	if !ok {
		http.Error(w, "Missing `q` field in form data", http.StatusBadRequest)
		return
	}

	query, err := parseQuery(queryStr[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.log.Info("Query", "metric", query.metric, "column", query.column, "db", db[0], query.whereKey, query.whereValue)

	promQuery := fmt.Sprintf("last_over_time(%s_%s{db=\"%s\", %s=\"%s\"}[1y])", query.metric, query.column, db[0], query.whereKey, query.whereValue)
	resp, _, err := h.prometheusAPI.Query(r.Context(), promQuery, time.Time{})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to query upstream - %s", err), http.StatusInternalServerError)
		return
	}

	influxResponse, err := func() (*influxDBV1Response, error) {
		var timestamp float64
		var value float64

		switch respTyped := resp.(type) {
		case *model.Scalar:
			timestamp = float64(respTyped.Timestamp.Unix())
			value = float64(respTyped.Value)
		case model.Vector:
			if len(respTyped) == 0 {
				return &influxDBV1Response{
					Results: []influxDBV1Result{
						{
							StatementID: 0,
						},
					},
				}, nil
			}

			if len(respTyped) > 1 {
				return nil, fmt.Errorf("Upstream query returned more than one value")
			}

			timestamp = float64(respTyped[0].Timestamp.Unix())
			value = float64(respTyped[0].Value)
		default:
			return nil, fmt.Errorf("Upstream response was not a scalar or vector - Actual type: %s", resp.Type())
		}

		h.log.Debug("Resp", "timestamp", timestamp, "value", value)

		return &influxDBV1Response{
			Results: []influxDBV1Result{
				{
					StatementID: 0,
					Series: []influxDBV1Series{
						{
							Name: query.metric,
							Columns: []string{
								"time",
								"last",
							},
							Values: [][]float64{
								{
									timestamp,
									value,
								},
							},
						},
					},
				},
			},
		}, nil
	}()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)

	encoder := json.NewEncoder(w)
	if err := encoder.Encode(influxResponse); err != nil {
		h.log.Error("Failed to encode influxResponse to caller - %v", err)
		panic(http.ErrAbortHandler)
	}
}

type influxDBV1Response struct {
	Results []influxDBV1Result `json:"results"`
}

type influxDBV1Result struct {
	StatementID int                `json:"statement_id"`
	Series      []influxDBV1Series `json:"series"`
}

type influxDBV1Series struct {
	Name    string      `json:"name"`
	Columns []string    `json:"columns"`
	Values  [][]float64 `json:"values"`
}
