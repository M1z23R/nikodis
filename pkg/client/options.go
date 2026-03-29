package client

import "google.golang.org/grpc"

type options struct {
	namespace string
	token     string
	dialOpts  []grpc.DialOption
}

type Option func(*options)

func WithNamespace(ns string) Option {
	return func(o *options) { o.namespace = ns }
}

func WithToken(token string) Option {
	return func(o *options) { o.token = token }
}

func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOpts = append(o.dialOpts, opts...) }
}
