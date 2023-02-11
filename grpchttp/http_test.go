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
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/ericchiang/grpc-http-go/grpchttp/internal/testservice"
)

type testServer struct {
	pb.UnimplementedTestServer
}

func (t *testServer) CreateItem(ctx context.Context, req *pb.CreateItemRequest) (*pb.Item, error) {
	return req.GetItem(), nil
}

func (t *testServer) GetItem(ctx context.Context, req *pb.GetItemRequest) (*pb.Item, error) {
	resp := &pb.Item{
		Name: req.GetName(),
		Id:   req.GetFilter().GetId(),
		Kind: req.GetFilter().GetKind(),
	}

	switch req.GetName() {
	case "myname":
	case "myname2":
	default:
		return nil, status.Errorf(codes.NotFound, "item not found")
	}
	return resp, nil
}

func (t *testServer) ListItems(ctx context.Context, req *emptypb.Empty) (*pb.ListItemsResponse, error) {
	return &pb.ListItemsResponse{
		Items: []*pb.Item{
			{Name: "myname", Id: 1},
			{Name: "myname2", Id: 2},
		},
	}, nil
}

func TestListItems(t *testing.T) {
	desc := &pb.Test_ServiceDesc
	srv := &testServer{}
	h, err := NewHandler(desc, srv)
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}

	want := &pb.ListItemsResponse{
		Items: []*pb.Item{
			{Name: "myname", Id: 1},
			{Name: "myname2", Id: 2},
		},
	}

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

	want := &pb.Item{Name: "myname", Kind: pb.ItemKind_WIDGET, Id: 1}

	body := &bytes.Buffer{}
	rr := httptest.NewRecorder()
	rr.Body = body
	urlPath := "/v1/items/myname?filter.id=1&filter.kind=WIDGET"
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

	want := &pb.Item{Name: "myname", Kind: pb.ItemKind_WIDGET, Id: 1}

	body := &bytes.Buffer{}
	rr := httptest.NewRecorder()
	rr.Body = body
	req := `{"name":"myname","kind":"WIDGET","id":1}`
	urlPath := "/v1/items"
	h.ServeHTTP(rr, httptest.NewRequest("POST", urlPath, strings.NewReader(req)))

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
