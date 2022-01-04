package grpcclient

import (
	"flag"
	"time"

	"github.com/go-kit/log"
	middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"

	"github.com/grafana/dskit/backoff"
	"github.com/grafana/dskit/crypto/tls"
	"github.com/grafana/dskit/grpcencoding/snappy"
)

// Config for a gRPC client.
type Config struct {
	MaxRecvMsgSize  int     `yaml:"max_recv_msg_size"`
	MaxSendMsgSize  int     `yaml:"max_send_msg_size"`
	GRPCCompression string  `yaml:"grpc_compression"`
	RateLimit       float64 `yaml:"rate_limit"`
	RateLimitBurst  int     `yaml:"rate_limit_burst"`

	BackoffOnRatelimits bool           `yaml:"backoff_on_ratelimits"`
	BackoffConfig       backoff.Config `yaml:"backoff_config"`

	TLSEnabled bool             `yaml:"tls_enabled"`
	TLS        tls.ClientConfig `yaml:",inline"`
}

// RegisterFlags registers flags.
func (cfg *Config) RegisterFlags(f *flag.FlagSet) {
	cfg.RegisterFlagsWithPrefix("", f)
}

// RegisterFlagsWithPrefix registers flags with prefix.
func (cfg *Config) RegisterFlagsWithPrefix(prefix string, f *flag.FlagSet) {
	f.IntVar(&cfg.MaxRecvMsgSize, prefix+".grpc-max-recv-msg-size", 100<<20, "gRPC client max receive message size (bytes).")
	f.IntVar(&cfg.MaxSendMsgSize, prefix+".grpc-max-send-msg-size", 16<<20, "gRPC client max send message size (bytes).")
	f.StringVar(&cfg.GRPCCompression, prefix+".grpc-compression", "", "Use compression when sending messages. Supported values are: 'gzip', 'snappy' and '' (disable compression)")
	f.Float64Var(&cfg.RateLimit, prefix+".grpc-client-rate-limit", 0., "Rate limit for gRPC client; 0 means disabled.")
	f.IntVar(&cfg.RateLimitBurst, prefix+".grpc-client-rate-limit-burst", 0, "Rate limit burst for gRPC client.")
	f.BoolVar(&cfg.BackoffOnRatelimits, prefix+".backoff-on-ratelimits", false, "Enable backoff and retry when we hit ratelimits.")
	f.BoolVar(&cfg.TLSEnabled, prefix+".tls-enabled", cfg.TLSEnabled, "Enable TLS in the GRPC client. This flag needs to be enabled when any other TLS flag is set. If set to false, insecure connection to gRPC server will be used.")

	cfg.BackoffConfig.RegisterFlagsWithPrefix(prefix, f)

	cfg.TLS.RegisterFlagsWithPrefix(prefix, f)
}

func (cfg *Config) Validate(log log.Logger) error {
	switch cfg.GRPCCompression {
	case gzip.Name, snappy.Name, "":
		// valid
	default:
		return errors.Errorf("unsupported compression type: %s", cfg.GRPCCompression)
	}
	return nil
}

// CallOptions returns the config in terms of CallOptions.
func (cfg *Config) CallOptions() []grpc.CallOption {
	var opts []grpc.CallOption
	opts = append(opts, grpc.MaxCallRecvMsgSize(cfg.MaxRecvMsgSize))
	opts = append(opts, grpc.MaxCallSendMsgSize(cfg.MaxSendMsgSize))
	if cfg.GRPCCompression != "" {
		opts = append(opts, grpc.UseCompressor(cfg.GRPCCompression))
	}
	return opts
}

// DialOption returns the config as a slice of grpc.DialOptions.
//
// keepaliveTime is the number of seconds after which the client will ping the server in case of inactivity.
// See `google.golang.org/grpc/keepalive.ClientParameters.Time` for reference.
//
// keepaliveTimeout is the number of seconds the client waits after pinging the server, and if no activity is
// seen after that, the connection is closed. See `google.golang.org/grpc/keepalive.ClientParameters.Timeout`
// for reference.
func (cfg *Config) DialOption(unaryClientInterceptors []grpc.UnaryClientInterceptor, streamClientInterceptors []grpc.StreamClientInterceptor, keepaliveTime, keepaliveTimeout int64) ([]grpc.DialOption, error) {
	opts, err := cfg.TLS.GetGRPCDialOptions(cfg.TLSEnabled)
	if err != nil {
		return nil, err
	}

	if cfg.BackoffOnRatelimits {
		unaryClientInterceptors = append([]grpc.UnaryClientInterceptor{NewBackoffRetry(cfg.BackoffConfig)}, unaryClientInterceptors...)
	}

	if cfg.RateLimit > 0 {
		unaryClientInterceptors = append([]grpc.UnaryClientInterceptor{NewRateLimiter(cfg)}, unaryClientInterceptors...)
	}

	return append(
		opts,
		withDefaultCallOptions(cfg.CallOptions()...),
		withUnaryInterceptor(middleware.ChainUnaryClient(unaryClientInterceptors...)),
		withStreamInterceptor(middleware.ChainStreamClient(streamClientInterceptors...)),
		withKeepaliveParams(keepalive.ClientParameters{
			Time:                time.Duration(keepaliveTime) * time.Second,
			Timeout:             time.Duration(keepaliveTimeout) * time.Second,
			PermitWithoutStream: true,
		}),
	), nil
}

var withDefaultCallOptions = func(cos ...grpc.CallOption) grpc.DialOption {
	return grpc.WithDefaultCallOptions(cos...)
}

var withUnaryInterceptor = func(f grpc.UnaryClientInterceptor) grpc.DialOption {
	return grpc.WithUnaryInterceptor(f)
}

var withStreamInterceptor = func(f grpc.StreamClientInterceptor) grpc.DialOption {
	return grpc.WithStreamInterceptor(f)
}

var withKeepaliveParams = func(kp keepalive.ClientParameters) grpc.DialOption {
	return grpc.WithKeepaliveParams(kp)
}
