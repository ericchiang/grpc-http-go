module github.com/ericchiang/grpc-http-go

go 1.23.0

toolchain go1.23.6

require (
	github.com/google/go-cmp v0.6.0
	google.golang.org/genproto/googleapis/api v0.0.0-20250227231956-55c901821b1e
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250227231956-55c901821b1e
	google.golang.org/grpc v1.70.0
	google.golang.org/protobuf v1.36.5
)

require (
	go.opentelemetry.io/otel v1.34.0 // indirect
	go.opentelemetry.io/otel/sdk v1.34.0 // indirect
	golang.org/x/net v0.37.0 // indirect
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.5.1 // indirect
)

tool (
	google.golang.org/grpc/cmd/protoc-gen-go-grpc
	google.golang.org/protobuf/cmd/protoc-gen-go
)
