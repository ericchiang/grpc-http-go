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

// Package grpchttp implements HTTP/JSON to gRPC transcoding.
//
// To use, define a gRPC service with HTTP bindings:
//
//	syntax = "proto3";
//
//	// ...
//
//	import "google/api/annotations.proto";
//
//	service Test {
//	  rpc GetItem(GetItemRequest) returns(Item) {
//	    option (google.api.http) = {
//	      get: "/v1/items/{name=*}"
//	    };
//	  }
//	}
//
//	message Item {
//	  string name = 1;
//	  int64 id = 2;
//	}
//
//	message GetItemRequest {
//	  string name = 1;
//	}
//
// After implememeting the service, the grpchttp package can be used to create
// a [net/http.Handler] that translates HTTP/JSON to gRPC:
//
//	srv := &myService{}
//	hander, err := grpchttp.NewHandler(&pb.Test_ServiceDesc, srv)
//	if err != nil {
//		// Handle error...
//	}
//	// Listen on port :8080 for HTTP/JSON requests and translate them to be
//	// served by 'srv'.
//	log.Fatal(http.ListenAndServe(":8080", handler))
//
// See [google.golang.org/genproto/googleapis/api/annotations.HttpRule] for a
// full list of supported annotation values.
//
// # Errors
//
// Errors are supported through gRPC's [google.golang.org/grpc/status] and
// [google.golang.org/grpc/codes] packages. Errors returned to users always use
// the [google.rpc.Status] message format, usually wrapping errors with an
// internal code.
//
// If a method returns a [google.golang.org/grpc/status] error (for example, by
// using [status.Errorf]) the error will be passed back to the user directly:
//
//	info := &errdetails.ErrorInfo{
//		Reason: "STOCKOUT",
//		Domain: "spanner.googleapis.com",
//		Metadata: map[string]string{
//			"availableRegions": "us-central1,us-east2",
//		},
//	}
//	any, err := anypb.New(info)
//	if err != nil {
//		// Handle error...
//	}
//	s := &statuspb.Status{
//		Code:    int(codes.ResourceExhausted),
//		Message: "Region out of stock",
//		Details: []&anypb.Any{any},
//	}
//	return nil, status.FromProto(s) // Response body contains Status message.
//
// See https://google.aip.dev/193 for guidance, details, and RPC status code
// mappings to HTTP statuses.
//
// [google.rpc.Status]: https://github.com/googleapis/googleapis/blob/master/google/rpc/status.proto
// [status.Errorf]: https://pkg.go.dev/google.golang.org/grpc/status#Errorf
package grpchttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Default number of bytes the handler will read from the body. This matches
// gRPC's default.
const defaultMaxRequestBodySize = 1024 * 1024 * 4

// Handler implements HTTP/JSON to gRPC transcoding based on google.api.Http
// annotations.
type Handler struct {
	rules map[string][]rule

	maxRequestBodySize int
}

// HandlerOption configures an option on the returned handler, such as limiting
// the number of bytes the handler will read.
type HandlerOption interface {
	update(*Handler)
}

type handerOptionFunc func(*Handler)

func (fn handerOptionFunc) update(o *Handler) {
	fn(o)
}

// MaxRequestBodySize returns a HandlerOption to set the max message size in
// bytes the server can receive. If this is not set, the handler uses the
// default 4MB.
func MaxRequestBodySize(m int) HandlerOption {
	return handerOptionFunc(func(o *Handler) {
		o.maxRequestBodySize = m
	})
}

// NewHandler evaluates google.api.Http annotations on a service definition and
// generates a HTTP handler for the service.
//
// This method returns an error if the service contains RPCs without google.api.http
// annotations, paths have overlapping templates ("GET /v1/*" and "GET /v1/users"), or
// annotations aren't well defined (get annotations with bodies).
//
// All service RPCs must have annotations.
func NewHandler(desc *grpc.ServiceDesc, srv interface{}, opts ...HandlerOption) (*Handler, error) {
	name := protoreflect.FullName(desc.ServiceName)
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to find descriptor for service %s: %v", name, err)
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("proto %s is not a service", name)
	}

	h := &Handler{
		rules: make(map[string][]rule),
	}

	sv := reflect.ValueOf(srv)
	methods := sd.Methods()
	for i := 0; i < methods.Len(); i++ {
		mdesc := methods.Get(i)
		fullName := mdesc.FullName()
		opt := mdesc.Options().ProtoReflect().Get(annotations.E_Http.TypeDescriptor())
		if !opt.IsValid() {
			return nil, fmt.Errorf("method %s has no 'option' annotations", fullName)
		}

		rule, ok := opt.Message().Interface().(*annotations.HttpRule)
		if !ok {
			return nil, fmt.Errorf("expected http rule for option on method %s, got %s", fullName, opt.Message().Interface())
		}

		m, err := newMethod(sv, mdesc)
		if err != nil {
			return nil, fmt.Errorf("evaluating method %s: %v", fullName, err)
		}
		if err := h.addRule(m, rule, false); err != nil {
			return nil, fmt.Errorf("evaluating rule for method %s: %v", fullName, err)
		}
	}

	for _, o := range opts {
		o.update(h)
	}
	return h, nil
}

var unimplementedResp = status.New(codes.Unimplemented, "Method not implemented").Proto()

// ServeHTTP accepts HTTP/JSON equivalents of gRPC service RPCs.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, rule := range h.rules[r.Method] {
		// TODO(ericchiang): There are probably more optimal ways to do this matching
		// (e.g. with a trie), but wildcard support makes it complicated. For now,
		// just loop through and match. In the future, optimize.
		vars, ok := rule.path.matches(r.URL.Path)
		if !ok {
			continue
		}
		h.handle(rule.method, w, r, vars, rule.body, rule.response)
		return
	}
	writeResponse(w, 501, unimplementedResp)
}

// addRule parses an HTTP rule, registering the handler with associated paths.
//
// additional is set to true when evaluating "additional_bindings" rules, which can only
// be nested a single level deep.
func (h *Handler) addRule(m *method, httpRule *annotations.HttpRule, additional bool) error {
	for _, r := range httpRule.GetAdditionalBindings() {
		if additional {
			return fmt.Errorf("nested bindings must not contain an additional_bindings field themselves")
		}
		if err := h.addRule(m, r, true); err != nil {
			return fmt.Errorf("processing additional_bindings: %v", err)
		}
	}

	var method, path string
	switch p := httpRule.GetPattern().(type) {
	case *annotations.HttpRule_Custom:
		if p.Custom == nil {
			return nil
		}
		method = p.Custom.Kind
		path = p.Custom.Path
	case *annotations.HttpRule_Delete:
		method = http.MethodDelete
		path = p.Delete
	case *annotations.HttpRule_Get:
		if httpRule.GetBody() != "" {
			return fmt.Errorf("body field specified with GET rule: %s", httpRule)
		}
		method = http.MethodGet
		path = p.Get
	case *annotations.HttpRule_Patch:
		method = http.MethodPatch
		path = p.Patch
	case *annotations.HttpRule_Post:
		method = http.MethodPost
		path = p.Post
	case *annotations.HttpRule_Put:
		method = http.MethodPut
		path = p.Put
	default:
		return nil
	}
	if path == "" {
		return fmt.Errorf("no path provided for: %s", httpRule)
	}
	if method == "" {
		return fmt.Errorf("no method provided for rule: %s", httpRule)
	}
	if httpRule.GetBody() != "" && httpRule.GetBody() != "*" {
		fd := m.in.Descriptor().Fields().ByTextName(httpRule.GetBody())
		if fd == nil {
			return fmt.Errorf("unrecognized 'body' value for input message %s: %s",
				m.in.Descriptor().FullName(), httpRule.GetBody())
		}
		if fd.Kind() != protoreflect.MessageKind {
			return fmt.Errorf("'body' value '%s' is not a message", httpRule.GetBody())
		}
	}

	pc, err := parsePathTemplate(path)
	if err != nil {
		return fmt.Errorf("parse path template %s: %v", path, err)
	}
	for _, r := range h.rules[method] {
		if pc.overlaps(r.path) {
			return fmt.Errorf("path %s and %s are ambigious", pc, r.path)
		}
	}
	h.rules[method] = append(h.rules[method], rule{
		path:     pc,
		method:   m,
		body:     httpRule.GetBody(),
		response: httpRule.GetResponseBody(),
	})
	return nil
}

var (
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType   = reflect.TypeOf((*error)(nil)).Elem()
)

// newMethod uses reflection to evaluate the function on the service matching the gRPC
// method description. It validates the method matches the expected signature,
// "Method(context.Context, *ReqT) (*RespT, error)", and returns a wrapped reflect.Func.
//
// Evaluation currently doesn't support "Method(context.Context, *ReqT, RespT) error"
// style methods.
func newMethod(srv reflect.Value, desc protoreflect.MethodDescriptor) (*method, error) {
	inType, err := protoregistry.GlobalTypes.FindMessageByName(desc.Input().FullName())
	if err != nil {
		return nil, fmt.Errorf("lookup input type %s: %v", desc.Input().FullName(), err)
	}
	outType, err := protoregistry.GlobalTypes.FindMessageByName(desc.Output().FullName())
	if err != nil {
		return nil, fmt.Errorf("lookup output type %s: %v", desc.Output().FullName(), err)
	}

	wantInType := reflect.TypeOf(inType.New().Interface())
	wantOutType := reflect.TypeOf(outType.New().Interface())

	fn := srv.MethodByName(string(desc.Name()))
	if fn.IsZero() {
		return nil, fmt.Errorf("implementation does not implement method: %s", desc.Name())
	}

	typ := fn.Type()
	if typ.NumIn() != 2 {
		return nil, fmt.Errorf("method %s does not take exactly two arguments", desc.Name())
	}
	if typ.NumOut() != 2 {
		return nil, fmt.Errorf("method %s does not return exactly two arguments", desc.Name())
	}

	want := [...]reflect.Type{
		contextType,
		wantInType,
		wantOutType,
		errorType,
	}
	got := [...]reflect.Type{
		typ.In(0),
		typ.In(1),
		typ.Out(0),
		typ.Out(1),
	}

	for i, t := range want {
		if t == got[i] {
			continue
		}
		return nil, fmt.Errorf("expected method %s(context.Context, %s) (%s, error), got %s(%s, %s) (%s, %s)",
			desc.Name(), wantInType, wantOutType,
			desc.Name(), typ.In(0), typ.In(1), typ.Out(0), typ.Out(1))
	}
	return &method{inType, fn}, nil
}

// rule is a parsed HttpRule annotation and associated method.
type rule struct {
	path     *pathTemplate
	method   *method
	body     string
	response string
}

// method holds a reflect references to the service's function and input
// parameters.
type method struct {
	in protoreflect.MessageType
	fn reflect.Value // Always kind Func.
}

// setValue sets a protobuf message to the provided parameter value (name=foo).
// If the field references a submessage (name.first=foo), setValue walks the
// message to set the correct field.
func setValue(msg protoreflect.Message, field, val string) error {
	field, rest, ok := strings.Cut(field, ".")
	fd := msg.Type().Descriptor().Fields().ByTextName(field)
	if fd == nil {
		return fmt.Errorf("message %s doesn't have a field name: %s", msg.Descriptor().FullName(), field)
	}

	var v protoreflect.Value
	if ok {
		if fd.Kind() != protoreflect.MessageKind {
			return fmt.Errorf("expected field %s to be a message, got %s", field, fd.Kind())
		}
		msg := msg.Mutable(fd).Message()
		if err := setValue(msg, rest, val); err != nil {
			return err
		}
		v = protoreflect.ValueOfMessage(msg)
	} else {
		switch fd.Kind() {
		case protoreflect.StringKind:
			v = protoreflect.ValueOfString(val)
		case protoreflect.Int64Kind:
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid int64 value: %s", val)
			}
			v = protoreflect.ValueOfInt64(n)
		case protoreflect.EnumKind:
			vd := fd.Enum().Values().ByName(protoreflect.Name(val))
			if vd == nil {
				return fmt.Errorf("enum %s does not have a value %s", fd.FullName(), val)
			}
			v = protoreflect.ValueOfEnum(vd.Number())
		default:
			return fmt.Errorf("unsupported proto kind: %s", fd.Kind())
		}
	}
	msg.Set(fd, v)
	return nil
}

// setBody parses the request body into the provided message. Body can either be
// the special value "*" or a message field. Subfields aren't supported (name.first).
func (h *Handler) setBody(msg protoreflect.Message, body string, w http.ResponseWriter, r *http.Request) error {
	if body == "" {
		return nil
	}

	maxSize := defaultMaxRequestBodySize
	if h.maxRequestBodySize > 0 {
		maxSize = h.maxRequestBodySize
	}

	reqBody := http.MaxBytesReader(w, r.Body, int64(maxSize))
	data, err := io.ReadAll(reqBody)
	reqBody.Close()

	if err != nil {
		var merr *http.MaxBytesError
		if errors.As(err, &merr) {
			return status.Errorf(codes.ResourceExhausted, "request body too large")
		}
		return fmt.Errorf("reading request body: %v", err)
	}

	if body == "*" {
		if err := protojson.Unmarshal(data, msg.Interface()); err != nil {
			return fmt.Errorf("parsing request body: %v", err)
		}
		return nil
	}

	fd := msg.Type().Descriptor().Fields().ByTextName(body)
	if fd == nil {
		return fmt.Errorf("unrecognized body: %s", body)
	}
	if fd.Kind() != protoreflect.MessageKind {
		return fmt.Errorf("expected field %s to be a message, got %s", body, fd.Kind())
	}
	field := msg.Mutable(fd).Message()
	if err := protojson.Unmarshal(data, field.Interface()); err != nil {
		return fmt.Errorf("parsing request body: %v", err)
	}
	v := protoreflect.ValueOfMessage(field)
	msg.Set(fd, v)
	return nil
}

// call invokes a gRPC method with the provided request body and parameters. "vars" is filled by the
// URL path. The boy and response parameters indicate which part of the message should be filled.
func (h *Handler) call(m *method, w http.ResponseWriter, r *http.Request, vars []varValue, body, response string) (proto.Message, error) {
	q := r.URL.Query()
	for _, v := range vars {
		if _, ok := q[v.name]; ok {
			return nil, status.Errorf(codes.InvalidArgument, "field supplied in url path and query: %s", v.name)
		}
	}

	for k, vv := range q {
		if len(vv) > 1 {
			return nil, status.Errorf(codes.InvalidArgument, "query key supplied twice: %s", k)
		}
		if len(vv) == 0 {
			continue
		}
		vars = append(vars, varValue{k, vv[0]})
	}

	msg := m.in.New()
	if err := h.setBody(msg, body, w, r); err != nil {
		return nil, err
	}

	for _, v := range vars {
		if err := setValue(msg, v.name, v.val); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "failed to set value %s on request: %v", v.name, err)
		}
	}

	in := []reflect.Value{
		reflect.ValueOf(r.Context()),
		reflect.ValueOf(msg.Interface()),
	}
	out := m.fn.Call(in)
	respVal := out[0]
	err := out[1]
	if !err.IsNil() {
		return nil, err.Interface().(error)
	}
	if respVal.IsNil() {
		return nil, status.Errorf(codes.Internal, "method returned nil response")
	}
	resp := respVal.Interface().(proto.Message)
	if response != "" {
		msg := resp.ProtoReflect()
		fd := msg.Descriptor().Fields().ByTextName(response)
		if fd == nil {
			return nil, status.Errorf(codes.Internal, "response does not have a %s field", response)
		}
		if fd.Kind() != protoreflect.MessageKind {
			return nil, status.Errorf(codes.Internal, "response field is not a message: %s", response)
		}
		resp = msg.Get(fd).Message().Interface()
	}
	return resp, nil
}

// codeToHTTP translates an RPC code to HTTP.
//
// See: https://github.com/googleapis/googleapis/blob/master/google/rpc/code.proto
var codeToHTTP = map[codes.Code]int{
	codes.Canceled:           499,
	codes.Unknown:            500,
	codes.InvalidArgument:    400,
	codes.DeadlineExceeded:   504,
	codes.NotFound:           404,
	codes.AlreadyExists:      409,
	codes.PermissionDenied:   403,
	codes.Unauthenticated:    401,
	codes.ResourceExhausted:  429,
	codes.FailedPrecondition: 400,
	codes.Aborted:            409,
	codes.OutOfRange:         400,
	codes.Unimplemented:      501,
	codes.Internal:           500,
	codes.Unavailable:        503,
	codes.DataLoss:           15,
}

// handle implements the core logic for calling the method and writing the response value.
func (h *Handler) handle(m *method, w http.ResponseWriter, r *http.Request, vars []varValue, body, response string) {
	statusCode := 200
	resp, err := h.call(m, w, r, vars, body, response)
	if err != nil {
		s, ok := status.FromError(err)
		if !ok {
			s = status.New(codes.Unknown, err.Error())
		}
		statusCode, ok = codeToHTTP[s.Code()]
		if !ok {
			statusCode = 500
		}
		resp = s.Proto()
	}
	writeResponse(w, statusCode, resp)
}

func writeResponse(w http.ResponseWriter, statusCode int, resp proto.Message) {
	data, err := protojson.Marshal(resp)
	if err != nil {
		statusCode = 500
		data = []byte(`{code:"INTERNAL",message:"failed to encode response"}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(statusCode)
	w.Write(data)
}

// pathTemplate is the parsed representation of a path template. Templates are
// implemented as a linked list, with each node corresponding to a single path
// element. The template '/foo/{x=bar/*}:spam' would be represented as the
// following nodes:
//
//	'foo' -> 'bar' -> '*'
//
// The final node holds variables and verbs of the template. For the example
// above, the variables would be:
//
//	pathVariable{name: "x", start: 1, end: 2}
//
// The verb would be ':spam'.
type pathTemplate struct {
	s string // Original string.

	// Exactly one of these will be set.
	wildcard       bool   // '*'
	doubleWildcard bool   // '**'
	ident          string // 'foo'

	// If this isn't the last path component, next will be not nil.
	next *pathTemplate

	// If next is nil, and this if the final element, the following
	// values will be set.

	// Verb of the path template without the ':'. For the template '/foo:bar', this
	// would hold 'bar'.
	verb string
	// Any varabiles and their associated indexes within the path.
	variables []pathVariable
}

// leaf returns the last path template.
func (p *pathTemplate) leaf() *pathTemplate {
	for p.next != nil {
		p = p.next
	}
	return p
}

// sameVerb determines if the two path templates have the same verb.
func (p *pathTemplate) sameVerb(p2 *pathTemplate) bool {
	return p.leaf().verb == p2.leaf().verb
}

// overlaps attempts to determine if two path templates could match the same
// path (/foo/bar, foo/*).
func (p *pathTemplate) overlaps(p2 *pathTemplate) bool {
	if p.doubleWildcard || p2.doubleWildcard {
		return p.sameVerb(p2)
	}
	if p.ident != "" && p2.ident != "" && p.ident != p2.ident {
		return false
	}
	if p.next == nil && p2.next == nil {
		return p.sameVerb(p2)
	}
	if p.next != nil && p2.next != nil {
		return p.next.overlaps(p2.next)
	}
	return false
}

// varValue holds a resolved value from a path template. If '/foo/{name}'
// matches '/foo/bar'. The varValue {name: "name", val: "bar"} will be returned.
type varValue struct {
	name string
	val  string
}

// matches indicates if a path template matches the URL path.
func (p *pathTemplate) matches(path string) ([]varValue, bool) {
	if !strings.HasPrefix(path, "/") {
		return nil, false
	}
	path = path[1:]

	// Perform the match on the full path, then walk the path nodes to
	// determine variable values.
	pc, ok := p.matchesPath(path)
	if !ok {
		return nil, false
	}

	// Path already matches and verbs can't have varbiales. If the path has a verb,
	// remove it.
	path, _, _ = strings.Cut(path, ":")

	var values []varValue
	for _, v := range pc.variables {
		p := path
		start := 0
		for i := 0; i < v.start; i++ {
			index := strings.IndexRune(p[start:], '/')
			if index < 0 {
				start = len(p)
			} else {
				start = start + index + 1
			}
		}

		end := start
		if v.end == endDoubleWildcard {
			end = len(p)
		} else {
			for i := v.start; i < v.end; i++ {
				index := strings.IndexRune(p[end+1:], '/')
				if index < 0 {
					end = len(p)
				} else {
					end = end + 1 + index
				}
			}
		}

		values = append(values, varValue{v.name, p[start:end]})
	}
	return values, true
}

// matchesPath iterates through each component of the pathTemplate, determining
// if the path matches the value.
//
// If the path matches, this method returns the leaf node.
func (p *pathTemplate) matchesPath(path string) (*pathTemplate, bool) {
	path, verb, foundVerb := strings.Cut(path, ":")
	for {
		curr, rest, found := strings.Cut(path, "/")
		if curr == "" {
			return nil, false
		}
		if p.next != nil {
			if !found {
				return nil, false
			}
			if p.wildcard || p.ident == curr {
				path = rest
				p = p.next
				continue
			}
			return nil, false
		}
		if !p.doubleWildcard && found {
			return nil, false
		}
		if p.ident != "" && p.ident != curr {
			return nil, false
		}
		if foundVerb && p.verb == "" {
			return nil, false
		}
		return p, verb == p.verb
	}
}

// len computes the number of components in a path template. The template
// '/foo/{bar=x/*}' would return 3.
func (p *pathTemplate) len() int {
	n := 1
	pc := p
	for pc.next != nil {
		n++
		pc = pc.next
	}
	return n
}

// String returns the string that was used to compile the path template.
func (p *pathTemplate) String() string {
	return p.s
}

// endDoubleWildcard is a special value indicating that the path variable ends
// with a double wildcard, not a specific index.
//
// A path such as '/foo/{bar=**}' would use this value.
const endDoubleWildcard = -1

// pathVariable holds an individual path variable, and what component it starts
// and ends at.
type pathVariable struct {
	name  string
	start int
	end   int
}

// eof is a special rune value used by the scanner.
const eof = 0

// scanner is a minimal rune reader.
type scanner struct {
	s    string
	last int
	n    int
}

// newScanner initializes a scanner from a given string.
//
// Scanners assume the input is a valid UTF-8 string.
func newScanner(s string) *scanner {
	return &scanner{s: s}
}

// next returns the next rune in the string, or eof.
func (s *scanner) next() rune {
	if s.n >= len(s.s) {
		return eof
	}
	r, size := utf8.DecodeRuneInString(s.s[s.n:])
	s.n += size
	return r
}

// peek returns the next rune in the string without advancing the scanner.
func (s *scanner) peek() rune {
	if s.n >= len(s.s) {
		return eof
	}
	r, _ := utf8.DecodeRuneInString(s.s[s.n:])
	return r
}

// skip causes the scanner to discard the pending string. It does not change
// the current index.
func (s *scanner) skip() {
	s.last = s.n
}

// string returns the current consumed string then skips to the last index.
func (s *scanner) string() string {
	last := s.last
	s.last = s.n
	return s.s[last:s.n]
}

// errorf returns a formatted string with additional context from the scanner.
func (s *scanner) errorf(format string, v ...any) error {
	return fmt.Errorf("failed parsing %s at pos %d: "+format, append([]any{s.s, s.n}, v...)...)
}

// parsePathTemplate parses a raw path template into its linked list form.
func parsePathTemplate(path string) (*pathTemplate, error) {
	p := &parser{newScanner(path)}
	return p.parse(path)
}

// parser is a path template parser.
//
// See: https://github.com/googleapis/googleapis/blob/master/google/api/http.proto
type parser struct {
	s *scanner
}

// parse compiles a path template.
func (p *parser) parse(path string) (*pathTemplate, error) {
	switch p.s.next() {
	case '/':
	case eof:
		return nil, p.s.errorf("path is empty")
	default:
		return nil, p.s.errorf("path template must start with '/'")
	}
	p.s.skip()
	pc, err := p.parseSegments(false)
	if err != nil {
		return nil, err
	}
	pc.s = path
	return pc, nil
}

// parseSegments parse a top level template, including segments and optional verb.
//
//	Template = "/" Segments [ Verb ] ;
//
// This is used within variables as well, but rejects recursive variables.
//
//	Variable = "{" FieldPath [ "=" Segments ] "}" ;
func (p *parser) parseSegments(inVariable bool) (*pathTemplate, error) {
	var (
		vars       []pathVariable
		verb       string
		head, tail *pathTemplate
	)
	for {
		r := p.s.peek()
		if r == eof {
			return nil, p.s.errorf("unexpected eof parsing path segment")
		}

		switch r {
		case '*', '{':
		default:
			if isReserved(r) {
				return nil, p.s.errorf("parsing path segments, unexpected character: '%c'", r)
			}
		}

		next, varName, err := p.parseSegment(inVariable)
		if err != nil {
			return nil, err
		}

		if varName != "" {
			if inVariable {
				return nil, p.s.errorf("recursive varariables detected")
			}
			start := 0
			if head != nil {
				start = head.len()
			}

			end := start + next.len()

			if next.leaf().doubleWildcard {
				end = endDoubleWildcard
			}

			pv := pathVariable{varName, start, end}
			vars = append(vars, pv)
		}

		if head == nil {
			head = next
		}
		if tail == nil {
			tail = next
		} else {
			tail.next = next
		}
		for tail.next != nil {
			tail = tail.next
		}

		r = p.s.peek()
		if r == ':' {
			p.s.next()
			p.s.skip()
			if err := p.parseIdent(); err != nil {
				return nil, err
			}
			verb = p.s.string()
			if p.s.peek() != eof {
				return nil, p.s.errorf("unexpected character after verb")
			}
			break
		}
		if r == eof {
			if inVariable {
				return nil, p.s.errorf("unmatched '{'")
			}
			break
		}
		if r == '}' && inVariable {
			p.s.next()
			p.s.skip()
			break
		}

		if r != '/' {
			return nil, p.s.errorf("unexpected character in path segment: '%c'", r)
		}
		p.s.next()
		p.s.skip()

	}
	if head == nil {
		return nil, p.s.errorf("empty path")
	}
	for curr := head; curr.next != nil; curr = curr.next {
		if curr.doubleWildcard {
			return nil, fmt.Errorf("'**' must be last element of path")
		}
	}
	tail.variables = vars
	tail.verb = verb
	return head, nil
}

// isReserved returns if a rune is one of the reserved characters used in a path
// template.
func isReserved(r rune) bool {
	switch r {
	case '/', '*', '{', '=', '}', '.', ':':
		return true
	default:
		return false
	}
}

func (p *parser) parseSegment(inVariable bool) (pc *pathTemplate, varName string, err error) {
	r := p.s.peek()
	switch r {
	case eof:
		return nil, "", p.s.errorf("expected segment, got unexpected eof")
	case '*':
		isDouble, err := p.parseWildcard()
		if err != nil {
			return nil, "", err
		}
		if isDouble {
			return &pathTemplate{doubleWildcard: true}, "", nil
		}
		return &pathTemplate{wildcard: true}, "", nil
	case '{':
		if inVariable {
			return nil, "", p.s.errorf("variable contains '{' character")
		}

		p.s.next()
		p.s.skip()
		if err := p.parseFieldPath(); err != nil {
			return nil, "", err
		}
		varName := p.s.string()
		var varPath *pathTemplate
		r := p.s.peek()
		switch r {
		case '=':
			p.s.next()
			p.s.skip()
			pc, err := p.parseSegments(true)
			if err != nil {
				return nil, "", fmt.Errorf("parsing variable segments: %v", err)
			}
			varPath = pc
		case '}':
			p.s.next()
			p.s.skip()
		default:
			return nil, "", p.s.errorf("unexpected character parsing variable: '%c'", r)
		}
		if varPath == nil {
			varPath = &pathTemplate{wildcard: true}
		}
		return varPath, varName, nil
	}
	if err := p.parseIdent(); err != nil {
		return nil, "", err
	}
	return &pathTemplate{ident: p.s.string()}, "", nil
}

// parseFieldPath consumes a field path. For the path '/foo/{f1.f2=*}', this
// would be used to consumed 'f1.f2'.
func (p *parser) parseFieldPath() error {
	for {
		if err := p.parseIdent(); err != nil {
			return err
		}
		if p.s.peek() != '.' {
			return nil
		}
		p.s.next()
	}
}

// parseIdent consumes an identifier up to the next reserved character, or eof.
func (p *parser) parseIdent() error {
	r := p.s.next()
	if r == eof {
		return p.s.errorf("unexpected eof expecting identifer")
	}
	if isReserved(r) {
		return p.s.errorf("unexpected character parsing identifer: '%c'", r)
	}
	for {
		switch p.s.peek() {
		case eof, '/', '.', '}', ':', '=':
			return nil
		case '{':
			return p.s.errorf("unexpected '{' when parsing identifier")
		case '*':
			return p.s.errorf("unexpected '*' when parsing identifier")
		default:
			p.s.next()
		}
	}
}

// parseWildcard consumes a wildcard (or double wildcard).
func (p *parser) parseWildcard() (double bool, err error) {
	p.s.next()
	switch p.s.peek() {
	case '/', ':', '}', eof:
		p.s.skip()
		return false, nil
	case '*':
		p.s.next()
		p.s.skip()
		return true, nil
	default:
		return false, p.s.errorf("expected '/', ':', '*', '}' or eof after '*'")
	}
}
