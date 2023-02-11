package main

import (
	"context"
	"log"
	"net/http"

	"github.com/ericchiang/grpc-http-go/grpchttp"

	pb "github.com/ericchiang/grpc-http-go/grpchttp/internal/testservice"
)

type testServer struct {
	pb.UnimplementedTestServer
}

func (s *testServer) GetItem(ctx context.Context, req *pb.GetItemRequest) (*pb.Item, error) {
	return &pb.Item{Name: req.GetName(), Id: 42}, nil
}

func main() {
	srv := &testServer{}
	handler, err := grpchttp.NewHandler(&pb.Test_ServiceDesc, srv)
	if err != nil {
		log.Fatalf("creating http handler: %v", err)
	}
	log.Fatal(http.ListenAndServe(":8080", handler))
}
