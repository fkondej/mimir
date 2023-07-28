// SPDX-License-Identifier: AGPL-3.0-only

package middleware

import (
	"context"

	"github.com/opentracing/opentracing-go"
	"github.com/weaveworks/common/httpgrpc"
	"github.com/weaveworks/common/server"
	"google.golang.org/grpc"
)

// InitGRPCMiddleware initializes the stuff
func InitGRPCMiddleware(cfg *server.Config) {
	cfg.GRPCMiddleware = append(cfg.GRPCMiddleware, OpenTracingHTTPGRPCUnaryServerInterceptor(opentracing.GlobalTracer()))
}

const httpGRPCHandleMethod = "/httpgrpc.HTTP/Handle"

// OpenTracingHTTPGRPCUnaryServerInterceptor returns a grpc.UnaryServerInterceptor suitable
// for use in a grpc.NewServer call.
//
// For example:
//
//	s := grpc.NewServer(
//	    ...,  // (existing ServerOptions)
//	    grpc.UnaryInterceptor(otgrpc.OpenTracingServerInterceptor(tracer)))
//
// All gRPC server spans will look for an OpenTracing SpanContext in the gRPC
// metadata; if found, the server span will act as the ChildOf that RPC
// SpanContext.
//
// Root or not, the server Span will be embedded in the context.Context for the
// application-specific gRPC handler(s) to access.
func OpenTracingHTTPGRPCUnaryServerInterceptor(tracer opentracing.Tracer) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		if info.FullMethod == httpGRPCHandleMethod {
			if httpGRPCReq, ok := req.(*httpgrpc.HTTPRequest); ok {
				reqURL := httpGRPCReq.GetUrl()
				reqMethod := httpGRPCReq.GetMethod()
				span := opentracing.SpanFromContext(ctx)
				span.SetTag("http.url", reqURL).SetTag("http.method", reqMethod)
				span, ctx = opentracing.StartSpanFromContextWithTracer(
					ctx, tracer, httpGRPCHandleMethod+reqURL,
				)
				span.SetTag("http.url", reqURL).SetTag("http.method", reqMethod)
				defer span.Finish()
			}
		}

		resp, err = handler(ctx, req)
		return resp, err
	}
}
