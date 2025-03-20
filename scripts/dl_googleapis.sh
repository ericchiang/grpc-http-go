#!/bin/bash -e

REPO_DIR="$( git rev-parse --show-toplevel )"
REF="ba85d2f14538a01bf00c9af30f56986610d0a9be"
URL="https://github.com/googleapis/googleapis/archive/${REF}.zip"

TEMP_DIR="$( mktemp -d )"
curl -o "${TEMP_DIR}/googleapis.zip" -L "${URL}"
rm -rf "${REPO_DIR}/third_party/googleapis"
mkdir -p "${REPO_DIR}/third_party/googleapis/google/api"
unzip "${TEMP_DIR}/googleapis.zip" -d "${TEMP_DIR}"
cp "${TEMP_DIR}"/googleapis-*/LICENSE "${REPO_DIR}/third_party/googleapis/LICENSE"
cp "${TEMP_DIR}"/googleapis-*/google/api/annotations.proto "${REPO_DIR}/third_party/googleapis/google/api"
cp "${TEMP_DIR}"/googleapis-*/google/api/field_behavior.proto "${REPO_DIR}/third_party/googleapis/google/api"
cp "${TEMP_DIR}"/googleapis-*/google/api/http.proto "${REPO_DIR}/third_party/googleapis/google/api"
rm -rf "${TEMP_DIR}"
