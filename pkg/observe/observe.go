package observe

import (
	"context"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

var (
	GRPCHandled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "grpc_server_handled_total",
		Help: "gRPC and Connect requests handled.",
	}, []string{"method", "code"})
	GRPCLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "grpc_server_handling_seconds",
		Help:    "gRPC and Connect request latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method"})
	ShadowWrites = promauto.NewCounter(prometheus.CounterOpts{
		Name: "shadow_writes_total",
		Help: "Shadow reported and desired writes.",
	})
	RabbitPublishErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "rabbitmq_publish_errors_total",
		Help: "RabbitMQ publish errors.",
	})
	Streams = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "shadow_streams",
		Help: "Open shadow streams.",
	})
	Up = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "infinimesh_up",
		Help: "1 while this process is serving /metrics.",
	})
)

// Start serves /metrics and, when OTEL_EXPORTER_OTLP_ENDPOINT is set, exports traces.
// METRICS_ADDR defaults to :2112.
func Start(service string) func() {
	srv := Listen(os.Getenv("METRICS_ADDR"))
	stop := Tracing(context.Background(), os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), service)
	return func() {
		stop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func Listen(addr string) *http.Server {
	if addr == "" {
		addr = ":2112"
	}
	Up.Set(1)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ListenAndServe() }()
	return srv
}

func Interceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			start := time.Now()
			res, err := next(ctx, req)
			code := "ok"
			if err != nil {
				code = connect.CodeOf(err).String()
			}
			GRPCHandled.WithLabelValues(req.Spec().Procedure, code).Inc()
			GRPCLatency.WithLabelValues(req.Spec().Procedure).Observe(time.Since(start).Seconds())
			return res, err
		}
	}
}

func Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, span := otel.Tracer("infinimesh").Start(ctx, info.FullMethod)
		defer span.End()
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err).String()
		GRPCHandled.WithLabelValues(info.FullMethod, code).Inc()
		GRPCLatency.WithLabelValues(info.FullMethod).Observe(time.Since(start).Seconds())
		return resp, err
	}
}

func Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, span := otel.Tracer("infinimesh").Start(ss.Context(), info.FullMethod)
		defer span.End()
		start := time.Now()
		err := handler(srv, &ctxStream{ServerStream: ss, ctx: ctx})
		code := status.Code(err).String()
		GRPCHandled.WithLabelValues(info.FullMethod, code).Inc()
		GRPCLatency.WithLabelValues(info.FullMethod).Observe(time.Since(start).Seconds())
		return err
	}
}

type ctxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *ctxStream) Context() context.Context { return s.ctx }

// Tracing starts an OTLP exporter when endpoint is set. An empty endpoint leaves the default noop tracer.
func Tracing(ctx context.Context, endpoint, service string) func() {
	if endpoint == "" {
		return func() {}
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return func() {}
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
	)
	otel.SetTracerProvider(tp)
	return func() { _ = tp.Shutdown(context.Background()) }
}
