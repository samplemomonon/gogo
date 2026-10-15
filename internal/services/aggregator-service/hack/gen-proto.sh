#!/usr/bin/env bash
# Regenerates the gRPC stubs in gen/ from the vendored contracts in proto/.
#
# The .proto files here are copies of the owning service's contract with only the
# go_package rewritten. That duplication is deliberate and matches
# product-service, which already vendors category and media the same way: a peer
# must be able to build and release without a coordinated change to the contract
# owner. The cost is that a contract change shows up as a decode error in a
# response rather than as a compile error, which is the trade the other services
# made too.
#
# The generated files are committed so building needs nothing but Go.
# Re-run this after editing a .proto file.
#
# Requires protoc plus the two Go plugins on PATH:
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
set -euo pipefail

cd "$(dirname "$0")/.."

# protoc ships the well-known types (timestamp.proto and friends) in its own
# include directory. A normal install has it next to the binary; a manually
# unpacked release needs PROTOC_INCLUDE pointing at it.
if [[ -z "${PROTOC_INCLUDE:-}" ]]; then
  protoc_include="$(dirname "$(command -v protoc)")/../include"
  if [[ -f "${protoc_include}/google/protobuf/timestamp.proto" ]]; then
    PROTOC_INCLUDE="${protoc_include}"
  fi
fi

args=(-I proto)
# -I may only be passed when the directory actually exists.
[[ -n "${PROTOC_INCLUDE:-}" ]] && args+=(-I "${PROTOC_INCLUDE}")

protoc "${args[@]}" \
  --go_out=. --go_opt=module=aggregator-service \
  --go-grpc_out=. --go-grpc_opt=module=aggregator-service \
  proto/category/v1/category.proto \
  proto/media/v1/media.proto \
  proto/product/v1/product.proto \
  proto/listing/v1/listing.proto \
  proto/search/v1/search.proto

echo "generated gen/{categorypb,mediapb,productpb,listingpb,searchpb}"
