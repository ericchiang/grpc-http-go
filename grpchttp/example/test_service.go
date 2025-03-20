// Copyright 2023 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"log"
	"net/http"

	"github.com/ericchiang/grpc-http-go/grpchttp"
	"google.golang.org/protobuf/proto"

	pb "github.com/ericchiang/grpc-http-go/grpchttp/internal/testservice"
)

type testServer struct {
	pb.UnimplementedTestServer
}

func (s *testServer) GetItem(ctx context.Context, req *pb.GetItemRequest) (*pb.Item, error) {
	return pb.Item_builder{
		Name: proto.String(req.GetName()),
		Id:   proto.Int64(42),
	}.Build(), nil
}

func main() {
	srv := &testServer{}
	handler, err := grpchttp.NewHandler(&pb.Test_ServiceDesc, srv)
	if err != nil {
		log.Fatalf("creating http handler: %v", err)
	}
	log.Fatal(http.ListenAndServe(":8080", handler))
}
