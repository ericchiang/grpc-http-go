#!/bin/bash -e

REPO_DIR="$( git rev-parse --show-toplevel )"
if [[ -d ${REPO_DIR}/bin/protoc ]]; then
  exit
fi

PB_VERSION="3.15.8"
PB_REL="https://github.com/protocolbuffers/protobuf/releases"

OS="linux"
if [[ "$( go env GOOS )" == "darwin" ]]; then
  OS="osx"
fi


curl -o "${REPO_DIR}/bin/protoc.zip" -L "${PB_REL}/download/v${PB_VERSION}/protoc-${PB_VERSION}-${OS}-x86_64.zip"
unzip "${REPO_DIR}/bin/protoc.zip" -d "${REPO_DIR}/bin/protoc"
