package grpchttp

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	pb "github.com/ericchiang/grpc-http-go/grpchttp/internal/testservice"
	"github.com/google/go-cmp/cmp"
)

func TestRequestFieldBehavior(t *testing.T) {
	testCases := []struct {
		name    string
		method  string
		input   proto.Message
		want    proto.Message
		wantErr *missingRequiredErr
	}{
		{
			name:   "Required supplied field",
			method: "GET",
			input: pb.Item_builder{
				RequiredField: proto.Int32(1),
			}.Build(),
			want: pb.Item_builder{
				RequiredField: proto.Int32(1),
			}.Build(),
		},
		{
			name:   "Missing required field",
			method: "POST",
			input:  pb.Item_builder{}.Build(),
			wantErr: &missingRequiredErr{
				field: "requiredField",
			},
		},
		{
			name:   "Missing required field on update",
			method: "PATCH",
			input:  pb.Item_builder{}.Build(),
			want:   pb.Item_builder{}.Build(),
		},
		{
			name:   "Output only field",
			method: "POST",
			input: pb.Item_builder{
				RequiredField:   proto.Int32(1),
				OutputOnlyField: proto.Int32(2),
				SubItem: pb.SubItem_builder{
					OutputOnlyField: proto.Int32(3),
				}.Build(),
			}.Build(),
			want: pb.Item_builder{
				RequiredField: proto.Int32(1),
				SubItem:       pb.SubItem_builder{}.Build(),
			}.Build(),
		},
		{
			name:   "Immutable create",
			method: "POST",
			input: pb.Item_builder{
				RequiredField:  proto.Int32(1),
				ImmutableField: proto.Int32(2),
			}.Build(),
			want: pb.Item_builder{
				RequiredField:  proto.Int32(1),
				ImmutableField: proto.Int32(2),
			}.Build(),
		},
		{
			name:   "Immutable update",
			method: "PATCH",
			input: pb.Item_builder{
				RequiredField:  proto.Int32(1),
				ImmutableField: proto.Int32(2),
			}.Build(),
			want: pb.Item_builder{
				RequiredField: proto.Int32(1),
			}.Build(),
		},
		{
			name:   "Input only",
			method: "POST",
			input: pb.Item_builder{
				RequiredField:  proto.Int32(1),
				InputOnlyField: proto.Int32(2),
			}.Build(),
			want: pb.Item_builder{
				RequiredField:  proto.Int32(1),
				InputOnlyField: proto.Int32(2),
			}.Build(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := proto.Clone(tc.input)
			err := applyRequestFieldBehavior(tc.method, got.ProtoReflect())
			if err != nil {
				if !reflect.DeepEqual(err, tc.wantErr) {
					t.Errorf("Returned unexpected error, got=%v, want=%v", err, tc.wantErr)
				}
				return
			}
			if tc.wantErr != nil {
				t.Fatalf("Expected error")
			}

			if diff := cmp.Diff(tc.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("Applied field behavior returned diff (-want, +got): %s", diff)
			}
		})
	}
}

func TestResponseFieldBehavior(t *testing.T) {
	testCases := []struct {
		name   string
		method string
		input  proto.Message
		want   proto.Message
	}{
		{
			name:   "Empty message",
			method: "GET",
			input:  pb.Item_builder{}.Build(),
			want:   pb.Item_builder{}.Build(),
		},
		{
			name:   "Input field",
			method: "GET",
			input: pb.Item_builder{
				RequiredField:   proto.Int32(2),
				OutputOnlyField: proto.Int32(3),
				InputOnlyField:  proto.Int32(4),
			}.Build(),
			want: pb.Item_builder{
				RequiredField:   proto.Int32(2),
				OutputOnlyField: proto.Int32(3),
			}.Build(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := proto.Clone(tc.input)
			err := applyResponseFieldBehavior(tc.method, got.ProtoReflect())
			if err != nil {
				t.Errorf("Apply returned unexpected error: %v", err)
				return
			}

			if diff := cmp.Diff(tc.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("Applied field behavior returned diff (-want, +got): %s", diff)
			}
		})
	}
}
