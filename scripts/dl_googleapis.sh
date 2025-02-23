#!/bin/bash -e

REPO_DIR="$( git rev-parse --show-toplevel )"
REF="d4da473c44a15ff7ae6931edbd1c6a354b21d323"
URL="https://github.com/googleapis/googleapis/archive/${REF}.zip"

TEMP_DIR="$( mktemp -d )"
curl -o "${TEMP_DIR}/googleapis.zip" -L "${URL}"
rm -rf "${REPO_DIR}/third_party/googleapis"
mkdir -p "${REPO_DIR}/third_party/googleapis/google/api"
unzip "${TEMP_DIR}/googleapis.zip" -d "${TEMP_DIR}"
cp "${TEMP_DIR}"/googleapis-*/LICENSE "${REPO_DIR}/third_party/googleapis/LICENSE"
cp "${TEMP_DIR}"/googleapis-*/google/api/annotations.proto "${REPO_DIR}/third_party/googleapis/google/api"
cp "${TEMP_DIR}"/googleapis-*/google/api/http.proto "${REPO_DIR}/third_party/googleapis/google/api"
rm -rf "${TEMP_DIR}"
