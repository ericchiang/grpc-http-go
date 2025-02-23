#!/bin/bash -e

REPO_DIR="$( git rev-parse --show-toplevel )"
"${REPO_DIR}/scripts/dl_protoc.sh"

PATH="${REPO_DIR}/bin:${PATH}" "${REPO_DIR}/bin/protoc/bin/protoc" \
	--go_out="${REPO_DIR}/grpchttp/internal/testservice" \
	--go_opt=paths=source_relative \
	--go_opt=Mtest.proto="${REPO_DIR}/grpchttp/internal/testservice" \
	--go-grpc_out="${REPO_DIR}/grpchttp/internal/testservice" \
	--go-grpc_opt=paths=source_relative \
	--go-grpc_opt=Mtest.proto="${REPO_DIR}/grpchttp/internal/testservice" \
	--proto_path="${REPO_DIR}/grpchttp/internal/testservice:${REPO_DIR}/bin/protoc/include:${REPO_DIR}/third_party/googleapis" \
	"${REPO_DIR}/grpchttp/internal/testservice/test.proto"
