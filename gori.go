// Package gori is a lightweight, embeddable LLM agent framework.
//
// The core has zero third-party dependencies. Provider backends live under
// provider/* and adapt the neutral Request/Response types to each vendor's wire
// format. Use Agent to run a provider against a session with a set of tools.
package gori

// Version is the framework version. Override at build time with
// -ldflags "-X github.com/leelsey/gori.Version=v0.1.0".
var Version = "dev"
