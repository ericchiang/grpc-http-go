// Package grpchttp implements HTTP/JSON to gRPC transcoding.
package grpchttp

import (
	"context"
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

// Handler implements HTTP/JSON to gRPC transcoding based on google.api.Http
// annotations.
//
//	import "google/api/annotations.proto";
//
//	service Messaging {
//	  rpc GetMessage(GetMessageRequest) returns (Message) {
//	    option (google.api.http) = {
//	      get: "/v1/{name=messages/*}"
//	    };
//	  }
//	}
//	message GetMessageRequest {
//	  string name = 1; // Mapped to URL path.
//	}
//	message Message {
//	  string text = 1; // The resource content.
//	}
//
// See [google.golang.org/genproto/googleapis/api/annotations.HttpRule] for a
// full list of supported annotation values.
type Handler struct {
	rules map[string][]rule
}

// NewHandler evaluates google.api.Http annotations on a service definition and
// generates a HTTP handler for the service.
//
// This method returns an error if the service contains RPCs without google.api.http
// annotations, paths have overlapping templates ("GET /v1/*" and "GET /v1/users"), or
// annotations aren't well defined (get annotations with bodies).
//
// All service RPCs must have annotations.
func NewHandler(desc *grpc.ServiceDesc, srv interface{}) (*Handler, error) {
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
		rule.method.handle(w, r, vars, rule.body, rule.response)
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
	path     *pathComponent
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
func setBody(msg protoreflect.Message, body string, r *http.Request) error {
	if body == "" {
		return nil
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("reading request body: %v", err)
	}

	if body == "*" {
		if err := protojson.Unmarshal(data, msg.Interface()); err != nil {
			return fmt.Errorf("parsing request body: %v", err)
		}
		return nil
	}

	fd := msg.Type().Descriptor().Fields().ByTextName(body)
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
func (m *method) call(r *http.Request, vars []varValue, body, response string) (proto.Message, error) {
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
	if err := setBody(msg, body, r); err != nil {
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
func (m *method) handle(w http.ResponseWriter, r *http.Request, vars []varValue, body, response string) {
	statusCode := 200
	resp, err := m.call(r, vars, body, response)
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

type pathComponent struct {
	s string // Original string.

	wildcard       bool
	doubleWildcard bool
	ident          string

	// If this isn't the last path component, next will be not nil.
	next *pathComponent

	// If next is nil, and this if the final element, the following
	// values will be set.

	verb      string
	variables []pathVariable
}

func (p *pathComponent) leaf() *pathComponent {
	for p.next != nil {
		p = p.next
	}
	return p
}

func (p *pathComponent) sameVerb(p2 *pathComponent) bool {
	return p.leaf().verb == p2.leaf().verb
}

// overlaps attempts to determine if two path templates could match the same
// path (/foo/bar, foo/*).
func (p *pathComponent) overlaps(p2 *pathComponent) bool {
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

type varValue struct {
	name string
	val  string
}

func (p *pathComponent) matches(path string) ([]varValue, bool) {
	if !strings.HasPrefix(path, "/") {
		return nil, false
	}
	path = path[1:]
	pc, ok := p.matchesPath(path)
	if !ok {
		return nil, false
	}

	// If the path has a verb, remove it.
	i := strings.LastIndex(path, ":")
	if i > 0 {
		path = path[:i]
	}

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
		if v.end == doubleWildcard {
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

func (p *pathComponent) matchesPath(path string) (*pathComponent, bool) {
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

func (p *pathComponent) len() int {
	n := 1
	pc := p
	for pc.next != nil {
		n++
		pc = pc.next
	}
	return n
}

func (p *pathComponent) string(b *strings.Builder) {
	if p.wildcard {
		b.WriteString("*")
	} else if p.doubleWildcard {
		b.WriteString("**")
	} else {
		b.WriteString(p.ident)
	}

	if p.next != nil {
		b.WriteString("/")
		p.next.string(b)
		return
	}
	if p.verb != "" {
		b.WriteString(":")
		b.WriteString(p.verb)
	}
	if len(p.variables) != 0 {
		b.WriteString(" (variables")
		for _, v := range p.variables {
			b.WriteString(" ")
			fmt.Fprintf(b, "%s{%d:%d}", v.name, v.start, v.end)
		}
		b.WriteString(")")
	}
}

func (p *pathComponent) String() string {
	return p.s
}

const doubleWildcard = -1

type pathVariable struct {
	name  string
	start int
	end   int
}

const eof = 0

type scanner struct {
	s    string
	last int
	n    int
}

func newScanner(s string) *scanner {
	return &scanner{s: s}
}

func (s *scanner) next() rune {
	if s.n >= len(s.s) {
		return eof
	}
	r, size := utf8.DecodeRuneInString(s.s[s.n:])
	s.n += size
	return r
}

func (s *scanner) peek() rune {
	if s.n >= len(s.s) {
		return eof
	}
	r, _ := utf8.DecodeRuneInString(s.s[s.n:])
	return r
}

func (s *scanner) skip() {
	s.last = s.n
}

func (s *scanner) string() string {
	last := s.last
	s.last = s.n
	return s.s[last:s.n]
}

func (s *scanner) errorf(format string, v ...any) error {
	return fmt.Errorf("failed parsing %s at pos %d: "+format, append([]any{s.s, s.n}, v...)...)
}

type parser struct {
	s *scanner
}

func (p *parser) parse(path string) (*pathComponent, error) {
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

func (p *parser) parseSegments(inVariable bool) (*pathComponent, error) {
	var (
		vars       []pathVariable
		verb       string
		head, tail *pathComponent
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
				end = doubleWildcard
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

func isReserved(r rune) bool {
	switch r {
	case '/', '*', '{', '=', '}', '.', ':':
		return true
	default:
		return false
	}
}

func (p *parser) parseSegment(inVariable bool) (pc *pathComponent, varName string, err error) {
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
			return &pathComponent{doubleWildcard: true}, "", nil
		}
		return &pathComponent{wildcard: true}, "", nil
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
		var varPath *pathComponent
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
			varPath = &pathComponent{wildcard: true}
		}
		return varPath, varName, nil
	}
	if err := p.parseIdent(); err != nil {
		return nil, "", err
	}
	return &pathComponent{ident: p.s.string()}, "", nil
}

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

func (p *parser) parseVerb() (string, error) {
	p.s.next()
	p.s.skip()
	for {
		switch r := p.s.next(); r {
		case eof:
			return p.s.string(), nil
		case '/', '.', '}', '{', '*', ':', '=':
			return "", p.s.errorf("reserved character '%c' used in verb", r)
		}
	}
}

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

func parsePathTemplate(path string) (*pathComponent, error) {
	p := &parser{newScanner(path)}
	return p.parse(path)
}
