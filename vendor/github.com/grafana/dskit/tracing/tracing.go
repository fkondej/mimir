// Provenance-includes-location: https://github.com/weaveworks/common/blob/main/tracing/tracing.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: Weaveworks Ltd.

package tracing

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/opentracing/opentracing-go"
	"github.com/pkg/errors"
	"github.com/uber/jaeger-client-go"
	jaegerpropagator "go.opentelemetry.io/contrib/propagators/jaeger"
	"go.opentelemetry.io/contrib/samplers/jaegerremote"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	bridge "go.opentelemetry.io/otel/bridge/opentracing"
	jaegerotel "go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/propagation"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var (
	// ErrBlankTraceConfiguration is an error to notify client to provide valid trace report agent or config server
	ErrBlankTraceConfiguration = errors.New("no trace report agent, config server, or collector endpoint specified")
	bridgeTracer               = bridge.NewBridgeTracer()
)

const (
	envJaegerAgentHost                 = "JAEGER_AGENT_HOST"
	envJaegerTags                      = "JAEGER_TAGS"
	envJaegerSamplerManagerHostPort    = "JAEGER_SAMPLER_MANAGER_HOST_PORT"
	envJaegerSamplerParam              = "JAEGER_SAMPLER_PARAM"
	envJaegerEndpoint                  = "JAEGER_ENDPOINT"
	envJaegerAgentPort                 = "JAEGER_AGENT_PORT"
	envJaegerSamplerType               = "JAEGER_SAMPLER_TYPE"
	envJaegerSamplingEndpoint          = "JAEGER_SAMPLING_ENDPOINT"
	envJaegerDefaultSamplingServerPort = 5778
	envJaegerDefaultUDPSpanServerHost  = "localhost"
	envJaegerDefaultUDPSpanServerPort  = "6831"
)

// NewFromEnv is a convenience function to allow tracing configuration
// via environment variables
//
// Tracing will be enabled if one (or more) of the following environment variables is used to configure trace reporting:
// - JAEGER_AGENT_HOST
// - JAEGER_SAMPLER_MANAGER_HOST_PORT
func NewFromEnv(serviceName string) (io.Closer, error) {
	cfg, err := parseTracingConfig()
	if err != nil {
		return nil, errors.Wrap(err, "could not load jaeger tracer configuration")
	}
	if cfg.samplingServerURL == "" && cfg.agentHostPort == "" && cfg.collectorEndpoint == "" {
		return nil, ErrBlankTraceConfiguration
	}
	return cfg.initJaegerTracerProvider(serviceName)
}

func GetBridgeTracer() *bridge.BridgeTracer {
	return bridgeTracer
}

// parseJaegerTags Parse Jaeger tags from env var JAEGER_TAGS, example of TAGs format: key1=value1,key2=${value2:value3} where value2 is an env var
// and value3 is the default value, which is optional.
func parseJaegerTags(sTags string) ([]attribute.KeyValue, error) {
	pairs := strings.Split(sTags, ",")
	res := make([]attribute.KeyValue, 0, len(pairs))
	for _, p := range pairs {
		k, v, found := strings.Cut(p, "=")
		if found {
			k, v := strings.TrimSpace(k), strings.TrimSpace(v)
			if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
				e, d, _ := strings.Cut(v[2:len(v)-1], ":")
				v = os.Getenv(e)
				if v == "" && d != "" {
					v = d
				}
			}
			if v == "" {
				return nil, errors.Errorf("invalid tag %q, expected key=value", p)
			}
			res = append(res, attribute.String(k, v))
		} else if p != "" {
			return nil, errors.Errorf("invalid tag %q, expected key=value", p)
		}
	}
	return res, nil
}

type config struct {
	agentHost         string
	collectorEndpoint string
	agentPort         string
	samplerType       string
	samplingServerURL string
	samplerParam      float64
	jaegerTags        []attribute.KeyValue
	agentHostPort     string
}

// parseTracingConfig facilitates initialization that is compatible with Jaeger's InitGlobalTracer method.
func parseTracingConfig() (config, error) {
	cfg := config{}
	var err error

	// Parse reporting agent configuration
	if e := os.Getenv(envJaegerEndpoint); e != "" {
		u, err := url.ParseRequestURI(e)
		if err != nil {
			return cfg, errors.Wrapf(err, "cannot parse env var %s=%s", envJaegerEndpoint, e)
		}
		cfg.collectorEndpoint = u.String()
	} else {
		useEnv := false
		host := envJaegerDefaultUDPSpanServerHost
		if e := os.Getenv(envJaegerAgentHost); e != "" {
			host = e
			useEnv = true
		}

		port := envJaegerDefaultUDPSpanServerPort
		if e := os.Getenv(envJaegerAgentPort); e != "" {
			port = e
			useEnv = true
		}

		if useEnv || cfg.agentHostPort == "" {
			cfg.agentHost = host
			cfg.agentPort = port
			cfg.agentHostPort = net.JoinHostPort(host, port)
		}
	}

	// Then parse the sampler Configuration
	if e := os.Getenv(envJaegerSamplerType); e != "" {
		cfg.samplerType = e
	}

	if e := os.Getenv(envJaegerSamplerParam); e != "" {
		if value, err := strconv.ParseFloat(e, 64); err == nil {
			if cfg.samplerParam >= 0 && cfg.samplerParam <= 1.0 {
				cfg.samplerParam = value
			}
			return cfg, fmt.Errorf(
				"invalid Param for probabilistic sampler; expecting value between 0 and 1, received %v",
				value,
			)
		}
		return cfg, errors.Wrapf(err, "cannot parse env var %s=%s", envJaegerSamplerParam, e)
	}

	if e := os.Getenv(envJaegerSamplingEndpoint); e != "" {
		cfg.samplingServerURL = e
	} else if e := os.Getenv(envJaegerSamplerManagerHostPort); e != "" {
		cfg.samplingServerURL = e
	} else if e := os.Getenv(envJaegerAgentHost); e != "" {
		// Fallback if we know the agent host - try the sampling endpoint there
		cfg.samplingServerURL = fmt.Sprintf("http://%s:%d/sampling", e, envJaegerDefaultSamplingServerPort)
	}

	// Parse tags
	cfg.jaegerTags, err = parseJaegerTags(os.Getenv(envJaegerTags))
	if err != nil {
		return cfg, errors.Wrapf(err, "could not parse %s", envJaegerTags)
	}
	return cfg, nil
}

// initJaegerTracerProvider initializes a new Jaeger Tracer Provider.
func (cfg config) initJaegerTracerProvider(serviceName string) (io.Closer, error) {
	// Read environment variables to configure Jaeger
	var ep jaegerotel.EndpointOption
	// Create the jaeger exporter: address can be either agent address (host:port) or collector Endpoint.
	if cfg.agentHostPort != "" {
		ep = jaegerotel.WithAgentEndpoint(
			jaegerotel.WithAgentHost(cfg.agentHost),
			jaegerotel.WithAgentPort(cfg.agentPort))
	} else {
		ep = jaegerotel.WithCollectorEndpoint(
			jaegerotel.WithEndpoint(cfg.collectorEndpoint))
	}
	exp, err := jaegerotel.New(ep)
	if err != nil {
		return nil, err
	}

	res, err := NewResource(serviceName, cfg.jaegerTags)
	if err != nil {
		return nil, err
	}

	// Configure sampling strategy
	sampler := tracesdk.AlwaysSample()
	if cfg.samplerType == "const" {
		if cfg.samplerParam == 0 {
			sampler = tracesdk.NeverSample()
		}
	} else if cfg.samplerType == "probabilistic" {
		tracesdk.TraceIDRatioBased(cfg.samplerParam)
	} else if cfg.samplerType == "remote" {
		sampler = jaegerremote.New(serviceName, jaegerremote.WithSamplingServerURL(cfg.samplingServerURL),
			jaegerremote.WithInitialSampler(tracesdk.TraceIDRatioBased(cfg.samplerParam)))
	} else if cfg.samplerType != "" {
		return nil, errors.Errorf("unknown sampler type %q", cfg.samplerType)
	}
	tp := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(exp),
		tracesdk.WithResource(res),
		tracesdk.WithSampler(sampler),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator([]propagation.TextMapPropagator{
		// w3c Propagator is the default propagator for opentelemetry
		propagation.TraceContext{}, propagation.Baggage{},
		// jaeger Propagator is for opentracing backwards compatibility
		jaegerpropagator.Jaeger{},
	}...))

	// OpenTracing <=> OpenTelemetry bridge
	// The tracer name is empty so that the bridge uses the default tracer.
	otTracer, _ := bridge.NewTracerPair(tp.Tracer(""))
	bridgeTracer = otTracer
	opentracing.SetGlobalTracer(otTracer)
	return &Closer{tp}, nil
}

type Closer struct {
	*tracesdk.TracerProvider
}

func (c Closer) Close() error {
	return c.Shutdown(context.Background())
}

// ExtractTraceID extracts the trace id, if any from the context.
func ExtractTraceID(ctx context.Context) (string, bool) {
	traceID, _ := ExtractSampledTraceID(ctx)
	return traceID, traceID != ""
}

// ExtractSampledTraceID works like ExtractTraceID but the returned bool is only
// true if the returned trace id is sampled.
func ExtractSampledTraceID(ctx context.Context) (string, bool) {
	// the common case, wehre jaeger and opentracing is used
	sp := opentracing.SpanFromContext(ctx)
	if sp != nil {
		sctx, ok := sp.Context().(jaeger.SpanContext)
		if ok {
			return sctx.TraceID().String(), sctx.IsSampled()
		}
	}

	// the case opentelemetry is used with or without bridge
	otelSp := trace.SpanFromContext(ctx)
	traceID, sampled := otelSp.SpanContext().TraceID(), otelSp.SpanContext().IsSampled()
	if traceID.IsValid() { // when noop span is used, the traceID is not valid
		return traceID.String(), sampled
	}

	return "", false
}
