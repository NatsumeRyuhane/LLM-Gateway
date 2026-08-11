// Package openai owns the strict public OpenAI-compatible HTTP, JSON, and SSE
// codec. It translates only between the accepted v0 wire surface and canonical
// protocol values; provider, routing, storage, authentication, and credential
// concerns deliberately remain outside this package.
package openai
