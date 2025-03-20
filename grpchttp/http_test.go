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

package grpchttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	pb "github.com/ericchiang/grpc-http-go/grpchttp/internal/testservice"
	errdetailspb "google.golang.org/genproto/googleapis/rpc/errdetails"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
)

type testServer struct {
	pb.UnimplementedTestServer
}

func (t *testServer) CreateItem(ctx context.Context, req *pb.CreateItemRequest) (*pb.Item, error) {
	return req.GetItem(), nil
}

func (t *testServer) GetItem(ctx context.Context, req *pb.GetItemRequest) (*pb.Item, error) {
	resp := pb.Item_builder{
		Name:     proto.String(req.GetName()),
		Id:       proto.Int64(req.GetFilter().GetId()),
		Kind:     req.GetFilter().GetKind().Enum(),
		ReadMask: req.GetReadMask(),
	}.Build()

	switch req.GetName() {
	case "myname":
	case "myname2":
	default:
		return nil, status.Errorf(codes.NotFound, "item not found")
	}
	return resp, nil
}

func (t *testServer) ListItems(ctx context.Context, req *emptypb.Empty) (*pb.ListItemsResponse, error) {
	return pb.ListItemsResponse_builder{
		Items: []*pb.Item{
			pb.Item_builder{
				Name: proto.String("myname"),
				Id:   proto.Int64(1),
			}.Build(),
			pb.Item_builder{
				Name: proto.String("myname2"),
				Id:   proto.Int64(2),
			}.Build(),
		},
	}.Build(), nil
}

func (t *testServer) TestResponseBody(ctx context.Context, req *emptypb.Empty) (*pb.TestResponseBodyResponse, error) {
	return pb.TestResponseBodyResponse_builder{
		Response: pb.TestResponseBodyResponse_Response_builder{
			Name: proto.String("foo"),
		}.Build(),
	}.Build(), nil
}

func TestListItems(t *testing.T) {
	desc := &pb.Test_ServiceDesc
	srv := &testServer{}
	h, err := NewHandler(desc, srv)
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}

	want := pb.ListItemsResponse_builder{
		Items: []*pb.Item{
			pb.Item_builder{
				Name: proto.String("myname"),
				Id:   proto.Int64(1),
			}.Build(),
			pb.Item_builder{
				Name: proto.String("myname2"),
				Id:   proto.Int64(2),
			}.Build(),
		},
	}.Build()

	body := &bytes.Buffer{}
	rr := httptest.NewRecorder()
	rr.Body = body
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/v1/items", nil))

	got := &pb.ListItemsResponse{}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if err := protojson.Unmarshal(data, got); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Fatalf("/v1/shelves returned unexpected diff (-want, +got): %s", diff)
	}
}

func TestGetItem(t *testing.T) {
	desc := &pb.Test_ServiceDesc
	srv := &testServer{}
	h, err := NewHandler(desc, srv)
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}

	want := pb.Item_builder{
		Name: proto.String("myname"),
		Kind: pb.ItemKind_WIDGET.Enum(),
		Id:   proto.Int64(1),
		ReadMask: &fieldmaskpb.FieldMask{
			Paths: []string{"name", "filter"},
		},
	}.Build()

	body := &bytes.Buffer{}
	rr := httptest.NewRecorder()
	rr.Body = body
	urlPath := "/v1/items/myname?filter.id=1&filter.kind=WIDGET&read_mask=name%2Cfilter"
	h.ServeHTTP(rr, httptest.NewRequest("GET", urlPath, nil))

	got := &pb.Item{}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if err := protojson.Unmarshal(data, got); err != nil {
		t.Fatalf("parsing response %s: %v", data, err)
	}
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Fatalf("/v1/shelves returned unexpected diff (-want, +got): %s", diff)
	}
}

func TestCreateItem(t *testing.T) {
	desc := &pb.Test_ServiceDesc
	srv := &testServer{}
	h, err := NewHandler(desc, srv)
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}

	want := pb.Item_builder{
		Name:          proto.String("myname"),
		Kind:          pb.ItemKind_WIDGET.Enum(),
		Id:            proto.Int64(1),
		RequiredField: proto.Int32(1),
	}.Build()

	testCases := []struct {
		method string
		path   string
		body   string
	}{
		{"POST", "/v1/items", `{"name":"myname","kind":"WIDGET","id":1,"requiredField":1}`},
		{"POST", "/test_body_star", `{"item":{"name":"myname","kind":"WIDGET","id":1,"requiredField":1}}`},
		{"PATCH", "/test_patch", `{"name":"myname","kind":"WIDGET","id":1,"requiredField":1}`},
		{"PUT", "/test_put", `{"name":"myname","kind":"WIDGET","id":1,"requiredField":1}`},
		{"CUSTOMMETHOD", "/test_custom", `{"name":"myname","kind":"WIDGET","id":1,"requiredField":1}`},
	}
	for _, tc := range testCases {
		body := &bytes.Buffer{}
		rr := httptest.NewRecorder()
		rr.Body = body
		h.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))

		got := &pb.Item{}
		data, err := io.ReadAll(body)
		if err != nil {
			t.Errorf("reading body for %s %s: %v", tc.method, tc.path, err)
			continue
		}
		if err := protojson.Unmarshal(data, got); err != nil {
			t.Errorf("parsing response for %s %s %s: %v", tc.method, tc.path, data, err)
			continue
		}
		if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
			t.Errorf("%s %s returned unexpected diff (-want, +got): %s", tc.method, tc.path, diff)
		}
	}
}

func TestResponseBody(t *testing.T) {
	desc := &pb.Test_ServiceDesc
	srv := &testServer{}
	h, err := NewHandler(desc, srv)
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}

	want := pb.TestResponseBodyResponse_Response_builder{
		Name: proto.String("foo"),
	}.Build()

	body := &bytes.Buffer{}
	rr := httptest.NewRecorder()
	rr.Body = body
	urlPath := "/test_response_body"
	h.ServeHTTP(rr, httptest.NewRequest("GET", urlPath, nil))

	got := &pb.TestResponseBodyResponse_Response{}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if err := protojson.Unmarshal(data, got); err != nil {
		t.Fatalf("parsing response %s: %v", data, err)
	}
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Fatalf("/test_response_body returned unexpected diff (-want, +got): %s", diff)
	}
}

type customErrServer struct {
	pb.UnimplementedTestServer
}

func (s *customErrServer) GetItem(ctx context.Context, req *pb.GetItemRequest) (*pb.Item, error) {
	info := &errdetailspb.ErrorInfo{
		Reason: "foo",
		Domain: "example.com",
		Metadata: map[string]string{
			"bar": "spam",
		},
	}
	any, err := anypb.New(info)
	if err != nil {
		return nil, fmt.Errorf("encoding any value: %v", err)
	}
	stat := &statuspb.Status{
		Code:    3, // InvalidArgument
		Message: "test",
		Details: []*anypb.Any{any},
	}
	return nil, status.ErrorProto(stat)
}

func TestCustomErr(t *testing.T) {
	desc := &pb.Test_ServiceDesc
	srv := &customErrServer{}
	h, err := NewHandler(desc, srv)
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}

	info := &errdetailspb.ErrorInfo{
		Reason: "foo",
		Domain: "example.com",
		Metadata: map[string]string{
			"bar": "spam",
		},
	}
	any, err := anypb.New(info)
	if err != nil {
		t.Fatalf("encoding any value: %v", err)
	}
	want := &statuspb.Status{
		Code:    3, // InvalidArgument
		Message: "test",
		Details: []*anypb.Any{any},
	}

	body := &bytes.Buffer{}
	rr := httptest.NewRecorder()
	rr.Body = body
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/v1/items/test", nil))

	if rr.Code != 400 {
		t.Errorf("response returned unexpected code, got=%d, want=%d", rr.Code, 400)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	got := &statuspb.Status{}
	if err := protojson.Unmarshal(data, got); err != nil {
		t.Fatalf("parsing response body: %v", err)
	}
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("request returned unexpected diff (-want, +got): %s", diff)
	}
}

func TestRequestTooLarge(t *testing.T) {
	desc := &pb.Test_ServiceDesc
	srv := &testServer{}
	opt := MaxRequestBodySize(1)
	h, err := NewHandler(desc, srv, opt)
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}

	want := &statuspb.Status{
		Code:    8, // ResourceExhausted
		Message: "request body too large",
	}

	reqBody := strings.NewReader(`{"name":"myname"}`)
	body := &bytes.Buffer{}
	rr := httptest.NewRecorder()
	rr.Body = body
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/v1/items", reqBody))

	if rr.Code != 429 {
		t.Errorf("response returned unexpected code, got=%d, want=%d", rr.Code, 429)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	got := &statuspb.Status{}
	if err := protojson.Unmarshal(data, got); err != nil {
		t.Fatalf("parsing response body: %v", err)
	}
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("request returned unexpected diff (-want, +got): %s", diff)
	}
}

func TestPathOverlap(t *testing.T) {
	testCases := []struct {
		p1, p2 string
		want   bool
	}{
		{"/a", "/a", true},
		{"/a", "/b", false},
		{"/a/b", "/a", false},
		{"/a:b", "/a:c", false},
		{"/a", "/*", true},
		{"/a/b", "/*", false},
		{"/a/b/c", "/**", true},
		{"/a/b", "/*/b", true},
		{"/a/b", "/*/c", false},
		{"/a/**", "/*", false},
		{"/a/**", "/**", true},
		{"/**:a", "/**:b", false},
	}
	for _, tc := range testCases {
		p1, err := parsePathTemplate(tc.p1)
		if err != nil {
			t.Errorf("parsePathTemplate(%s) returned error: %v", tc.p1, err)
		}
		p2, err := parsePathTemplate(tc.p2)
		if err != nil {
			t.Errorf("parsePathTemplate(%s) returned error: %v", tc.p2, err)
		}
		got := p1.overlaps(p2)
		if got != tc.want {
			t.Errorf("%s overlaps %s returned unexpected result, got=%v, want=%v", tc.p1, tc.p2, got, tc.want)
		}

	}
}

// testPathMatchCases defines test cases for TestPathMatch, and seeds the
// fuzzer corpus.
var testPathMatchCases = []struct {
	path     string
	match    string
	want     bool
	wantVars []varValue
}{
	{"/v1", "/v1", true, nil},
	{"/v1/users", "/v1/user", false, nil},
	{"/v1/users", "/v1/users", true, nil},
	{"/v1/*", "/v1/users", true, nil},
	{"/v1/*:put", "/v1/users", false, nil},
	{"/v1/*:put", "/v1/users:put", true, nil},
	{"/v1/*/foo", "/v1//foo", false, nil},
	{"/v1/*/b/*/d", "/v1/a/b/c/d", true, nil},
	{"/v1/*/b/*/d", "/v1/a/d/c/d", false, nil},
	{"/v1/{user}", "/v1/foo", true, []varValue{{"user", "foo"}}},
	{"/v1/{user}:put", "/v1/foo:put", true, []varValue{{"user", "foo"}}},
	{"/v1/{user}/bar", "/v1/foo", false, nil},
	{"/v1/{user}/bar", "/v1/foo/bar", true, []varValue{{"user", "foo"}}},
	{"/v1/{user=foo/*}/baz", "/v1/foo/bar/baz", true, []varValue{{"user", "foo/bar"}}},
	{"/v1/foo/{user=**}", "/v1/foo/bar/baz", true, []varValue{{"user", "bar/baz"}}},
	{"/v1/{aa}/{bb}/bar", "/v1/cc/dd/bar", true, []varValue{{"aa", "cc"}, {"bb", "dd"}}},
	{"/v1/{aa}/{bb}", "/v1/cc/", false, nil},
}

func TestPathMatch(t *testing.T) {
	for _, tc := range testPathMatchCases {
		tmpl, err := parsePathTemplate(tc.path)
		if err != nil {
			t.Errorf("parsePathTempate(%s) returned error: %v", tc.path, err)
			continue
		}

		gotVars, got := tmpl.matches(tc.match)
		if got != tc.want {
			t.Errorf("pathTemplate(%s).matches(%s) returned unexpected result, got=%v, want=%v",
				tc.path, tc.match, got, tc.want)
			continue
		}
		opt := cmp.AllowUnexported(varValue{})
		if diff := cmp.Diff(tc.wantVars, gotVars, opt); diff != "" {
			t.Errorf("pathTemplate(%s).matches(%s) returned unexpected diff (-want, +got): %s",
				tc.path, tc.match, diff)
		}
	}
}

func TestParsePathTemplate(t *testing.T) {
	testCases := []struct {
		path string
		want string
	}{
		{"v1/user", "path template must start with '/'"},
		{"/", "unexpected eof parsing path segment"},
		{"/.", "unexpected character: '.'"},
		{"/v1.", "unexpected character in path segment: '.'"},
		{"/v1/*", ""},
		{"/v1/**", ""},
		{"/v1/**/", "unexpected eof parsing path segment"},
		{"/v1/**/user", "'**' must be last element of path"},
		{"/v1/user", ""},
		{"/v1/user*", "unexpected '*' when parsing identifier"},
		{"/v1/user:put", ""},
		{"/v1/user:put{}", "unexpected '{' when parsing identifier"},
		{"/v1/user}", "unexpected character in path segment: '}'"},
		{"/v1/{user}", ""},
		{"/v1/{user}/bar", ""},
		{"/v1/{user.name}", ""},
		{"/v1/=user", "unexpected character: '='"},
		{"/v1/{user=*}/bar", ""},
		{"/v1/{user=**}", ""},
		{"/v1/{user=**}/bar", "'**' must be last element of path"},
		{"/v1/{user=foo}/bar", ""},
		{"/v1/{user=foo/}bar", "unexpected character: '}'"},
		{"/v1/{user=foo*}/bar", "unexpected '*' when parsing identifier"},
		{"/v1/{user=foo/*}/bar", ""},
		{"/v1/{user.foo=bar/name}/bar", ""},
	}
	for _, tc := range testCases {
		_, err := parsePathTemplate(tc.path)
		if err != nil {
			if tc.want == "" {
				t.Errorf("parsePathTemplate(%s) failed: %v", tc.path, err)
				continue
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("parsePathTemplate(%s) returned unexpected error, got=%v, want=%v", tc.path, err, tc.want)
			}
			continue
		}
		if tc.want != "" {
			t.Errorf("parsePathTemplate(%s) returned unexpected error, got=<nil>, want=%v", tc.path, tc.want)
		}
	}
}

func FuzzPathTemplate(f *testing.F) {
	f.Add("/v1/{name=messages/*}", "/v1/foo/bar")
	for _, tc := range testPathMatchCases {
		f.Add(tc.path, tc.match)
	}
	f.Fuzz(func(t *testing.T, template, pattern string) {
		tmpl, err := parsePathTemplate(template)
		if err != nil {
			return
		}
		tmpl.matches(pattern)
	})
}
