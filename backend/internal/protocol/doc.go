// Package protocol owns the provider-neutral gateway.adapter.v0 Chat Completions
// domain model. It validates canonical requests and responses, derives route
// capabilities from request semantics, and enforces the canonical stream state
// machine. HTTP codecs, provider wire formats, credentials, and routing policy
// deliberately live outside this package.
package protocol
