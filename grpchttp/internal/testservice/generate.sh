#!/bin/bash -e
# Copyright 2023 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


OS="osx"
if [[ "$( uname )" == "Linux" ]]; then
  OS="linux"
fi

if [ ! -f out/protoc.zip ]; then
    PB_REL="https://github.com/protocolbuffers/protobuf/releases"
    mkdir -p out
    curl -o out/protoc.zip -L "${PB_REL}/download/v3.15.8/protoc-3.15.8-${OS}-x86_64.zip"
fi

if [ ! -f out/protoc/bin/protoc ]; then
    mkdir -p out/protoc
    unzip out/protoc.zip -d out/protoc
fi

if [ ! -d out/include/googleapis ]; then
    mkdir -p out/include
    git clone https://github.com/googleapis/googleapis out/include/googleapis
    cd out/include/googleapis
    git checkout 2c7756f6228b12867e88496f047dff6331712799
    cd -
fi

PATH="${PWD}/bin:${PWD}/out:${PATH}" ./out/protoc/bin/protoc \
    --go_out=. \
    --go_opt=paths=source_relative \
    --go_opt=Mtest.proto=../testservice \
    --go-grpc_out=. \
    --go-grpc_opt=paths=source_relative \
    --go-grpc_opt=Mtest.proto=../testservice \
    --proto_path=.:./out/include/googleapis:./out/protoc/include \
    test.proto
