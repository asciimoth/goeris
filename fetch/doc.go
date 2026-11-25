// Package fetch reads ERIS Fetch Protocol v1 source descriptions, response
// frames, and local route configuration. ParseSource accepts extension types
// so a router can inspect Type without knowing their schema. Use Source.Validate
// in a handler to check the schema and native file paths.
//
// Source values can contain secrets. A Failure reports a code and an optional
// field name. ParseSource and ParseResponse also preserve errors from the
// input reader, which can include sensitive details. Invalid response frames
// return ErrProtocol. Invalid routing files return ErrConfig.
//
// ParseJSON and the encoding/json package read standard descriptions.
// JSON encoding preserves the source ID and the order of list values.
// Source.ID hashes the canonical records. LookupRoute finds the first
// matching configuration. It reads no file below the selected route.
//
// ParseResponse verifies the complete frame. Response.ValidateFor checks it
// against the request, and Response.Proposal assembles one selected choice.
// Handlers can use Response.MarshalText to write a result.
//
// Tree uses the ERIS-FS builder for deterministic flat indexes and
// optional ERIS-FS-AUTO-1 file compression. Use eris.EncodeContentRecommended
// for blobs. Storage, lock updates, retrieval, and transport belong to the caller.
package fetch
