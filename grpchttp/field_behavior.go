package grpchttp

import (
	"fmt"
	"strconv"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// https://google.aip.dev/203

type missingRequiredErr struct {
	field string
}

func (m *missingRequiredErr) Error() string {
	return fmt.Sprintf("request missing required field: %s", m.field)
}

func (m *missingRequiredErr) GRPCStatus() *status.Status {
	return status.New(codes.InvalidArgument, m.Error())
}

// applyRequestFieldBehavior clears any fields that shouldn't be set during a
// request, and validates required fields.
func applyRequestFieldBehavior(method string, m protoreflect.Message) error {
	if err := applyFieldBehavior(true, method, m); err != nil {
		return err
	}
	// https://go.dev/doc/faq#nil_error
	return nil
}

func applyResponseFieldBehavior(method string, m protoreflect.Message) error {
	if err := applyFieldBehavior(false, method, m); err != nil {
		return err
	}
	// https://go.dev/doc/faq#nil_error
	return nil
}

func applyFieldBehavior(isReq bool, method string, msg protoreflect.Message) *missingRequiredErr {
	isResp := !isReq
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() == protoreflect.MessageKind {
			if fd.Cardinality() == protoreflect.Repeated {
				list := msg.Get(fd).List()
				for i := 0; i < list.Len(); i++ {
					if err := applyFieldBehavior(isReq, method, list.Get(i).Message()); err != nil {
						err.field = fd.JSONName() + "[" + strconv.Itoa(i) + "]." + err.field
						return err
					}
				}
			} else {
				if err := applyFieldBehavior(isReq, method, msg.Get(fd).Message()); err != nil {
					err.field = fd.JSONName() + "." + err.field
					return err
				}
			}
		}

		opts := fd.Options().(*descriptorpb.FieldOptions)
		if opts == nil {
			continue
		}
		behaviors, ok := proto.GetExtension(opts, annotations.E_FieldBehavior).([]annotations.FieldBehavior)
		if !ok {
			continue
		}
		for _, fb := range behaviors {
			switch fb {
			case annotations.FieldBehavior_INPUT_ONLY:
				// https://google.aip.dev/203#input-only
				if isResp {
					msg.Clear(fd)
				}
			case annotations.FieldBehavior_IMMUTABLE:
				// https://google.aip.dev/203#immutable
				if isReq && method != "POST" {
					msg.Clear(fd)
				}
			case annotations.FieldBehavior_IDENTIFIER:
				// Identifier fields are not accepted as input during a create
				// operation.
				//
				// https://google.aip.dev/203#identifier
				if isReq && method == "POST" {
					msg.Clear(fd)
				}
			case annotations.FieldBehavior_OUTPUT_ONLY:
				// https://google.aip.dev/203#output-only
				if isReq {
					msg.Clear(fd)
				}
			case annotations.FieldBehavior_REQUIRED:
				// https://google.aip.dev/203#required
				if isReq && !msg.Has(fd) && method == "POST" {
					return &missingRequiredErr{field: fd.JSONName()}
				}
			}
		}
	}
	return nil
}
