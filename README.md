# gRPC HTTP annotations for Go

A Go implementation of [gRPC to HTTP/JSON transcoding][grpc-to-http] using
Google's [HTTP Protobuf annotations][grpc-http-annotations].

To use, define a gRPC service with HTTP bindings:

```proto
syntax = "proto3";

// ...

import "google/api/annotations.proto";

service Test {
  rpc GetItem(GetItemRequest) returns(Item) {
    option (google.api.http) = { 
      get: "/v1/items/{name=*}"
    };
  } 
}

message Item {
  string name = 1;
  int64 id = 2;
}

message GetItemRequest {
  string name = 1;
}
```

Generate the Protobuf and gRPC packages using [gRPC's Go support][grpc-go].
After implememeting the service, the grpchttp package can be used to create a
[net/http.Handler][http-handler]:

```go
package main

import (
    "context"
	"log"
    "net/http"

    "github.com/ericchiang/grpc-http-go/grpchttp"

    pb "example.com/testservice"
)

type service struct {}

func (s *service) GetItem(ctx context.Context, req *pb.GetItemRequest) (*pb.Item, error) {
    return &pb.Item{Name: req.GetName(), Id: 42}, nil
}

func main() {
    srv := &service{}
    hander, err := grpchttp.NewHandler(srv, &pb.Test_ServiceDesc)
    if err != nil {
        log.Fatalf("creating HTTP handler: %v", err)
    }
    log.Fatal(http.ListenAndServe(":8080", handler))
}
```

Request and responses will automatically be converted using the annotation rules
and Protobuf's JSON support:

```
$ curl -s http://localhost:8080/v1/items/myitem | jq .
{
  "name": "myitem",
  "id": "42"
}
```

This project is compariable to gRPC to HTTP/JSON support that exists
[gRPC-Gateway][grpc-gateway] and [Envoy][envoy], which are aim at larger scale
deployments of gRPC services. The grpchttp package aims to be a good solution
for single binaries written in Go to quickly support HTTP bindings without
custom proto generators.

[envoy]: https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/grpc_json_transcoder_filter
[grpc-gateway]: https://github.com/grpc-ecosystem/grpc-gateway
[grpc-go]: https://grpc.io/docs/languages/go/basics/
[grpc-http-annotations]: https://github.com/googleapis/googleapis/blob/master/google/api/http.proto
[grpc-to-http]: https://cloud.google.com/endpoints/docs/grpc/transcoding
[http-handler]: https://pkg.go.dev/net/http#Handler
