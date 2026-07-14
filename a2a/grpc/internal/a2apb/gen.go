// Package a2apb contains the Go bindings generated from the official A2A
// protobuf service definition (spec: a2aproject/A2A specification/a2a.proto,
// package lf.a2a.v1).
//
// The committed a2a.proto here is the official proto with the google.api.*
// gateway/field annotations stripped (they only drive HTTP transcoding, which
// gori serves separately via the JSON-RPC/HTTP binding) so generation needs no
// googleapis protos and pulls no extra dependency.
//
// Regenerate after updating a2a.proto:
//
//	protoc --proto_path=. \
//	  --go_out=. --go_opt=paths=source_relative \
//	  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
//	  a2a.proto
//
// (requires protoc plus protoc-gen-go and protoc-gen-go-grpc on PATH)
package a2apb
